//go:build linux

package linux

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goflasher/goflasher/internal/device"
	"golang.org/x/sys/unix"
)

const helperExecutable = "/usr/libexec/goflasher-helper"
const embeddedHelperArgument = "--goflasher-privileged-helper"

var ErrAuthorizationCanceled = errors.New("authorization canceled")

type operationMode string

const (
	modeWrite       operationMode = "write"
	modeRead        operationMode = "read-back"
	modeFlush       operationMode = "flush"
	modeFormatFAT32 operationMode = "format-fat32"
)

// privilegedRequest deliberately has no device-path field. The privileged side
// derives both sysfs and /dev names from the expected kernel device number.
type privilegedRequest struct {
	Identity string        `json:"identity"`
	Serial   string        `json:"serial,omitempty"`
	WWN      string        `json:"wwn,omitempty"`
	Major    uint32        `json:"major"`
	Minor    uint32        `json:"minor"`
	Capacity uint64        `json:"capacity"`
	Mode     operationMode `json:"mode"`
	Label    string        `json:"label,omitempty"`
}

func helperRequest(d device.Device, mode operationMode) privilegedRequest {
	return privilegedRequest{Identity: d.ID, Serial: d.Serial, WWN: d.WWN, Major: d.Major, Minor: d.Minor, Capacity: d.Size, Mode: mode}
}

type privilegedHelper interface {
	OpenWriter(context.Context, privilegedRequest) (io.WriteCloser, error)
	OpenReader(context.Context, privilegedRequest) (io.ReadCloser, error)
	Flush(context.Context, privilegedRequest) error
	FormatFAT32(context.Context, privilegedRequest) error
}

type commandHelper struct {
	executable string
	arguments  []string
}

func newCommandHelper() privilegedHelper {
	if info, err := os.Stat(helperExecutable); err == nil && info.Mode().IsRegular() {
		return &commandHelper{executable: helperExecutable}
	}
	executable, err := os.Executable()
	if err != nil {
		executable = helperExecutable
	}
	return &commandHelper{executable: executable, arguments: []string{embeddedHelperArgument}}
}

// IsEmbeddedHelperInvocation reports whether this process was started by the
// GUI through pkexec to serve one privileged operation.
func IsEmbeddedHelperInvocation(args []string) bool {
	return len(args) == 2 && args[1] == embeddedHelperArgument
}

func (h *commandHelper) start(ctx context.Context, r privilegedRequest) (*exec.Cmd, io.WriteCloser, io.ReadCloser, *strings.Builder, error) {
	pkexecArgs := append([]string{h.executable}, h.arguments...)
	cmd := exec.CommandContext(ctx, "pkexec", pkexecArgs...)
	in, out, err := commandPipes(cmd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stderr := &strings.Builder{}
	cmd.Stderr = stderr
	if err = cmd.Start(); err != nil {
		return nil, nil, nil, stderr, authorizationError(err, stderr.String())
	}
	if err = sendRequest(cmd, in, r); err != nil {
		return nil, nil, nil, stderr, err
	}
	buffered := bufio.NewReader(out)
	if err = awaitReady(cmd, in, buffered, stderr); err != nil {
		return nil, nil, nil, stderr, err
	}
	return cmd, in, &bufferedReadCloser{Reader: buffered, closer: out}, stderr, nil
}

func commandPipes(cmd *exec.Cmd) (io.WriteCloser, io.ReadCloser, error) {
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, nil, err
	}
	return in, out, nil
}

func sendRequest(cmd *exec.Cmd, in io.WriteCloser, request privilegedRequest) error {
	data, err := json.Marshal(request)
	if err == nil {
		_, err = in.Write(data)
	}
	if err != nil {
		_ = in.Close()
		_ = cmd.Wait()
		return err
	}
	return nil
}

func awaitReady(cmd *exec.Cmd, in io.WriteCloser, out *bufio.Reader, stderr *strings.Builder) error {
	line, readErr := out.ReadString('\n')
	if readErr == nil && line == "OK\n" {
		return nil
	}
	_ = in.Close()
	waitErr := cmd.Wait()
	if line != "" {
		stderr.WriteString(strings.TrimSpace(line))
	}
	if waitErr == nil {
		waitErr = readErr
	}
	return authorizationError(waitErr, stderr.String())
}

type bufferedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *bufferedReadCloser) Close() error { return r.closer.Close() }

func authorizationError(err error, detail string) error {
	if authorizationCanceled(err, detail) {
		return fmt.Errorf("%w: %v", ErrAuthorizationCanceled, err)
	}
	if detail != "" {
		return fmt.Errorf("privileged helper: %s: %w", strings.TrimSpace(detail), err)
	}
	return err
}

func authorizationCanceled(err error, detail string) bool {
	if strings.Contains(strings.ToLower(detail), "cancel") {
		return true
	}
	return err != nil && strings.Contains(err.Error(), "126")
}

type processWriter struct {
	io.WriteCloser
	cmd    *exec.Cmd
	stderr *strings.Builder
}

func (w *processWriter) Close() error {
	a := w.WriteCloser.Close()
	b := w.cmd.Wait()
	if a != nil {
		return a
	}
	if b == nil {
		return nil
	}
	return authorizationError(b, w.stderr.String())
}

type processReader struct {
	io.ReadCloser
	input io.WriteCloser
	cmd   *exec.Cmd
}

func (r *processReader) Close() error {
	a := r.ReadCloser.Close()
	_ = r.input.Close()
	// Verification normally reads only the image length, not unused space at the
	// end of the device. Terminate the one-shot helper if it is still streaming.
	_ = r.cmd.Process.Kill()
	_ = r.cmd.Wait()
	if a != nil {
		return a
	}
	return nil
}

func (h *commandHelper) OpenWriter(ctx context.Context, r privilegedRequest) (io.WriteCloser, error) {
	cmd, in, out, stderr, err := h.start(ctx, r)
	if out != nil {
		_ = out.Close()
	}
	if err != nil {
		return nil, err
	}
	return &processWriter{WriteCloser: in, cmd: cmd, stderr: stderr}, nil
}
func (h *commandHelper) OpenReader(ctx context.Context, r privilegedRequest) (io.ReadCloser, error) {
	cmd, in, out, _, err := h.start(ctx, r)
	if err != nil {
		return nil, err
	}
	_ = in.Close()
	return &processReader{ReadCloser: out, input: in, cmd: cmd}, nil
}
func (h *commandHelper) Flush(ctx context.Context, r privilegedRequest) error {
	cmd, in, out, _, err := h.start(ctx, r)
	if err != nil {
		return err
	}
	_ = in.Close()
	_ = out.Close()
	return cmd.Wait()
}

func (h *commandHelper) FormatFAT32(ctx context.Context, r privilegedRequest) error {
	cmd, in, out, stderr, err := h.start(ctx, r)
	if err != nil {
		return err
	}
	_ = in.Close()
	_ = out.Close()
	if err := cmd.Wait(); err != nil {
		return authorizationError(err, stderr.String())
	}
	return nil
}

type helperEnvironment struct{ SysDevBlock, SysClassBlock, MountInfo, Swaps, DevRoot string }

func realHelperEnvironment() helperEnvironment {
	return helperEnvironment{"/sys/dev/block", "/sys/class/block", "/proc/self/mountinfo", "/proc/swaps", "/dev"}
}

// RunPrivilegedHelper serves exactly one authenticated operation over stdin/stdout.
func RunPrivilegedHelper(in io.Reader, out io.Writer, errOut io.Writer) error {
	return runPrivilegedHelper(in, out, errOut, realHelperEnvironment())
}

func runPrivilegedHelper(in io.Reader, out io.Writer, errOut io.Writer, env helperEnvironment) error {
	dec := json.NewDecoder(in)
	dec.DisallowUnknownFields()
	var req privilegedRequest
	if err := dec.Decode(&req); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	f, err := validateAndOpen(req, env)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = io.WriteString(out, "OK\n"); err != nil {
		return err
	}
	switch req.Mode {
	case modeWrite:
		err = writeAndSync(f, dec.Buffered(), in)
	case modeRead:
		_, err = io.CopyN(out, f, int64(req.Capacity))
	case modeFlush:
		err = flushAndInvalidate(f, func() error { return invalidateBlockCache(f) })
	case modeFormatFAT32:
		err = makeFAT32(f, req.Capacity, req.Label, errOut)
	default:
		err = errors.New("unsupported operation mode")
	}
	return err
}

func flushAndInvalidate(target interface{ Sync() error }, invalidate func() error) error {
	if err := target.Sync(); err != nil {
		return err
	}
	return invalidate()
}

func invalidateBlockCache(target *os.File) error {
	// Verification must read the USB media, not clean pages retained from the
	// preceding write. BLKFLSBUF invalidates this block device's buffer cache;
	// the helper runs with the privilege required by the ioctl.
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, target.Fd(), uintptr(unix.BLKFLSBUF), 0)
	if errno != 0 {
		return fmt.Errorf("invalidate block cache: %w", errno)
	}
	return nil
}

func writeAndSync(target interface {
	io.Writer
	Sync() error
}, buffered, remaining io.Reader) error {
	if _, err := io.Copy(target, buffered); err != nil {
		return err
	}
	if _, err := io.Copy(target, remaining); err != nil {
		return err
	}
	return target.Sync()
}

func validateAndOpen(req privilegedRequest, env helperEnvironment) (*os.File, error) {
	if !req.valid() {
		return nil, errors.New("incomplete request")
	}
	name, real, err := resolveDevice(req, env)
	if err != nil {
		return nil, err
	}
	class := filepath.Join(env.SysClassBlock, name)
	major, minor, err := validateDeviceMetadata(req, class, real)
	if err != nil {
		return nil, err
	}
	if err := validateDeviceSafety(env, name, major, minor); err != nil {
		return nil, err
	}
	return openDevice(req, env, name)
}

func (req privilegedRequest) valid() bool {
	if req.Identity == "" {
		return false
	}
	if req.Capacity == 0 {
		return false
	}
	switch req.Mode {
	case modeWrite, modeRead, modeFlush:
		return true
	case modeFormatFAT32:
		return validFATLabel(req.Label)
	default:
		return false
	}
}

func validFATLabel(label string) bool {
	if label == "" || len(label) > 11 {
		return false
	}
	for _, r := range label {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func makeFAT32(device *os.File, size uint64, label string, _ io.Writer) error {
	return formatFAT32(device, size, label)
}

type randomAccessSyncer interface {
	io.WriterAt
	Sync() error
}

// formatFAT32 creates a standards-compatible FAT32 "superfloppy" directly.
// Keeping this small formatter in-process avoids executing distribution tools
// as root and makes packaged formatting independent of dosfstools.
func formatFAT32(device randomAccessSyncer, size uint64, label string) error {
	const sectorSize, reserved, fatCount = uint64(512), uint64(32), uint64(2)
	if size < 64<<20 || size/sectorSize > uint64(^uint32(0)) {
		return errors.New("device size is not supported by FAT32")
	}
	totalSectors := size / sectorSize
	sectorsPerCluster := fat32SectorsPerCluster(size)
	// The usual fixed-point iteration can oscillate forever between adjacent
	// sector counts for valid device sizes. Find the smallest FAT that can hold
	// its resulting cluster count with a bounded monotonic search instead.
	low := uint64(1)
	high := (((totalSectors-reserved)/sectorsPerCluster+2)*4 + sectorSize - 1) / sectorSize
	for low < high {
		middle := low + (high-low)/2
		clusters := (totalSectors - reserved - fatCount*middle) / sectorsPerCluster
		required := ((clusters+2)*4 + sectorSize - 1) / sectorSize
		if required <= middle {
			high = middle
		} else {
			low = middle + 1
		}
	}
	fatSectors := low
	dataSectors := totalSectors - reserved - fatCount*fatSectors
	clusters := dataSectors / sectorsPerCluster
	if clusters < 65525 {
		return errors.New("device is too small for FAT32")
	}
	// Clear both GPT metadata areas before creating the filesystem. Merely
	// replacing sector zero and the backup GPT header leaves partition-entry
	// arrays behind, which recovery tools can mistake for a still-valid layout.
	// The first 32 sectors are FAT32's reserved area and are rewritten below;
	// the final 33 sectors are outside the filesystem's allocated clusters.
	if err := writeFullAt(device, make([]byte, reserved*sectorSize), 0); err != nil {
		return err
	}
	if err := writeFullAt(device, make([]byte, 33*sectorSize), int64((totalSectors-33)*sectorSize)); err != nil {
		return err
	}

	boot := fat32BootSector(uint32(totalSectors), uint32(fatSectors), byte(sectorsPerCluster), label)
	fsinfo := fat32FSInfo(uint32(clusters))
	for _, write := range []struct {
		offset int64
		data   []byte
	}{{0, boot}, {512, fsinfo}, {6 * 512, boot}, {7 * 512, fsinfo}} {
		if err := writeFullAt(device, write.data, write.offset); err != nil {
			return err
		}
	}
	fat := make([]byte, fatSectors*sectorSize)
	binary.LittleEndian.PutUint32(fat[0:4], 0x0ffffff8)
	binary.LittleEndian.PutUint32(fat[4:8], 0x0fffffff)
	binary.LittleEndian.PutUint32(fat[8:12], 0x0fffffff)
	for copyIndex := uint64(0); copyIndex < fatCount; copyIndex++ {
		offset := int64((reserved + copyIndex*fatSectors) * sectorSize)
		if err := writeFullAt(device, fat, offset); err != nil {
			return err
		}
	}
	root := make([]byte, sectorsPerCluster*sectorSize)
	copy(root[:11], fatLabel(label))
	root[11] = 0x08
	rootOffset := int64((reserved + fatCount*fatSectors) * sectorSize)
	if err := writeFullAt(device, root, rootOffset); err != nil {
		return err
	}
	return device.Sync()
}

func writeFullAt(target io.WriterAt, data []byte, offset int64) error {
	for len(data) > 0 {
		n, err := target.WriteAt(data, offset)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
		offset += int64(n)
	}
	return nil
}

func fat32SectorsPerCluster(size uint64) uint64 {
	switch {
	case size <= 260<<20:
		return 1
	case size <= 8<<30:
		return 8
	case size <= 16<<30:
		return 16
	case size <= 32<<30:
		return 32
	default:
		return 64
	}
}

func fatLabel(label string) []byte {
	value := []byte("           ")
	copy(value, label)
	return value
}

func fat32BootSector(totalSectors, fatSectors uint32, sectorsPerCluster byte, label string) []byte {
	b := make([]byte, 512)
	copy(b[0:3], []byte{0xeb, 0x58, 0x90})
	copy(b[3:11], "GOFLASH ")
	binary.LittleEndian.PutUint16(b[11:13], 512)
	b[13] = sectorsPerCluster
	binary.LittleEndian.PutUint16(b[14:16], 32)
	b[16] = 2
	b[21] = 0xf8
	binary.LittleEndian.PutUint16(b[24:26], 63)
	binary.LittleEndian.PutUint16(b[26:28], 255)
	binary.LittleEndian.PutUint32(b[32:36], totalSectors)
	binary.LittleEndian.PutUint32(b[36:40], fatSectors)
	binary.LittleEndian.PutUint32(b[44:48], 2)
	binary.LittleEndian.PutUint16(b[48:50], 1)
	binary.LittleEndian.PutUint16(b[50:52], 6)
	b[64], b[66] = 0x80, 0x29
	binary.LittleEndian.PutUint32(b[67:71], 0x47464c53)
	copy(b[71:82], fatLabel(label))
	copy(b[82:90], "FAT32   ")
	b[510], b[511] = 0x55, 0xaa
	return b
}

func fat32FSInfo(clusters uint32) []byte {
	b := make([]byte, 512)
	binary.LittleEndian.PutUint32(b[0:4], 0x41615252)
	binary.LittleEndian.PutUint32(b[484:488], 0x61417272)
	binary.LittleEndian.PutUint32(b[488:492], clusters-1)
	binary.LittleEndian.PutUint32(b[492:496], 3)
	binary.LittleEndian.PutUint32(b[508:512], 0xaa550000)
	return b
}

func resolveDevice(req privilegedRequest, env helperEnvironment) (string, string, error) {
	link := filepath.Join(env.SysDevBlock, fmt.Sprintf("%d:%d", req.Major, req.Minor))
	real, err := filepath.EvalSymlinks(link)
	if err != nil {
		return "", "", ErrDeviceChanged
	}
	name := filepath.Base(real)
	class := filepath.Join(env.SysClassBlock, name)
	if exists(filepath.Join(class, "partition")) {
		return "", "", ErrUnsupportedDevice
	}
	if !supportedUSBDevice(class, real) {
		return "", "", ErrUnsupportedDevice
	}
	return name, real, nil
}

func supportedUSBDevice(class, real string) bool {
	if readTrim(filepath.Join(class, "removable")) != "1" {
		return false
	}
	if readTrim(filepath.Join(class, "device/type")) != "0" {
		return false
	}
	return strings.Contains(real, "/usb")
}

func validateDeviceMetadata(req privilegedRequest, class, real string) (uint32, uint32, error) {
	major, minor, err := readDeviceNumber(filepath.Join(class, "dev"))
	if err != nil {
		return 0, 0, ErrDeviceChanged
	}
	if major != req.Major || minor != req.Minor {
		return 0, 0, ErrDeviceChanged
	}
	if readUint(filepath.Join(class, "size"))*512 != req.Capacity {
		return 0, 0, ErrDeviceChanged
	}
	serials := uniqueIdentityValues(readTrim(filepath.Join(class, "device/serial")), readUSBAncestorAttribute(real, "serial"))
	wwns := uniqueIdentityValues(readTrim(filepath.Join(class, "wwid")), readTrim(filepath.Join(class, "device/wwid")))
	if req.Serial != "" && !containsIdentity(serials, req.Serial) {
		return 0, 0, ErrDeviceChanged
	}
	if req.WWN != "" && !containsIdentity(wwns, req.WWN) {
		return 0, 0, ErrDeviceChanged
	}
	identities := append(append(serials, wwns...), fmt.Sprintf("%d:%d@%s", major, minor, real))
	if !containsIdentity(identities, req.Identity) {
		return 0, 0, ErrDeviceChanged
	}
	return major, minor, nil
}

func uniqueIdentityValues(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !containsIdentity(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func containsIdentity(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// Some USB flash drives expose their serial only on the USB device node, while
// udev propagates it to ID_SERIAL_SHORT for the block device. Walk to the first
// physical USB ancestor so privileged revalidation uses the same hardware
// identity without trusting data supplied by the GUI.
func readUSBAncestorAttribute(real, attribute string) string {
	for path := filepath.Clean(real); path != filepath.Dir(path); path = filepath.Dir(path) {
		if exists(filepath.Join(path, "idVendor")) && exists(filepath.Join(path, "idProduct")) {
			return readTrim(filepath.Join(path, attribute))
		}
	}
	return ""
}

func validateDeviceSafety(env helperEnvironment, name string, major, minor uint32) error {
	unsafe, err := mountedOrSystem(name, major, minor, env)
	if err != nil {
		return err
	}
	if unsafe {
		return ErrSystemDisk
	}
	return validateDeviceNode(env, name, major, minor)
}

func validateDeviceNode(env helperEnvironment, name string, major, minor uint32) error {
	node := filepath.Join(env.DevRoot, name)
	var st syscall.Stat_t
	if err := syscall.Stat(node, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		return ErrDeviceChanged
	}
	if unix.Major(uint64(st.Rdev)) != major || unix.Minor(uint64(st.Rdev)) != minor {
		return ErrDeviceChanged
	}
	return nil
}

func openDevice(req privilegedRequest, env helperEnvironment, name string) (*os.File, error) {
	flags := os.O_RDONLY
	if req.Mode == modeFormatFAT32 {
		// The in-process formatter uses random-access writes and syncs the
		// completed filesystem, so its validated descriptor must be read/write.
		flags = os.O_RDWR
	} else if req.Mode == modeWrite {
		// Keep each large stream write tied to actual device progress instead of
		// letting Linux accept the entire image into the page cache at RAM speed.
		// Besides producing meaningful throughput/ETA figures, this bounds the
		// otherwise very long and apparently frozen Sync at the end of a write.
		flags = os.O_WRONLY | syscall.O_SYNC
	} else if req.Mode != modeRead {
		flags = os.O_WRONLY
	}
	return os.OpenFile(filepath.Join(env.DevRoot, name), flags|syscall.O_CLOEXEC, 0)
}

func mountedOrSystem(name string, major, minor uint32, env helperEnvironment) (bool, error) {
	mounts, err := parseMountInfo(env.MountInfo)
	if err != nil {
		return false, err
	}
	if deviceMounted(name, major, minor, env.SysClassBlock, mounts) {
		return true, nil
	}
	swaps, err := parseSwaps(env.Swaps)
	if err != nil {
		return false, err
	}
	return deviceUsedForSwap(name, env.SysClassBlock, swaps), nil
}

func deviceMounted(name string, major, minor uint32, classRoot string, mounts map[devNumber][]string) bool {
	for number, points := range mounts {
		if len(points) == 0 {
			continue
		}
		if sameDevice(number, major, minor) || parentName(classRoot, number) == name {
			return true
		}
	}
	return false
}

func sameDevice(number devNumber, major, minor uint32) bool {
	return number.major == major && number.minor == minor
}

func deviceUsedForSwap(name, classRoot string, swaps map[string]bool) bool {
	for path := range swaps {
		if filepath.Base(path) == name || parentForPath(classRoot, path) == name {
			return true
		}
	}
	return false
}
