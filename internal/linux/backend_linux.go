//go:build linux

package linux

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/device"
)

var (
	ErrUnsupportedDevice = errors.New("unsupported device")
	ErrSystemDisk        = errors.New("system disk")
	ErrDeviceChanged     = errors.New("device identity changed")
	ErrUnmountFailed     = errors.New("unmount failed")
)

// maxGenericUSBFlashSize is a conservative fallback for removable USB mass
// storage that udev does not tag with ID_DRIVE_THUMB or ID_DRIVE_FLASH.  Use
// decimal GB here to match the capacity printed on consumer flash drives.
const maxGenericUSBFlashSize uint64 = 128_000_000_000

type commandRunner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}
type osRunner struct{}

func (osRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	// udisksctl writes its useful diagnostics to stderr. Keep stdout and stderr
	// together so GUI errors explain the actual failure instead of only showing
	// "exit status 1".
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Backend enumerates Linux block devices from sysfs. udev is supplementary:
// candidates must satisfy kernel topology and conservative udev classification.
type Backend struct {
	SysClassBlock string
	MountInfo     string
	Swaps         string
	DevRoot       string
	runner        commandRunner
	helper        privilegedHelper
}

var _ device.Backend = (*Backend)(nil)

func NewBackend() *Backend {
	return &Backend{SysClassBlock: "/sys/class/block", MountInfo: "/proc/self/mountinfo", Swaps: "/proc/swaps", DevRoot: "/dev", runner: osRunner{}, helper: newCommandHelper()}
}

func (b *Backend) ListAllowedDevices(ctx context.Context) ([]device.Device, error) {
	all, err := b.list(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]device.Device, 0, len(all))
	for _, d := range all {
		if d.IsAllowed {
			allowed = append(allowed, d)
		}
	}
	return allowed, nil
}

func (b *Backend) list(ctx context.Context) ([]device.Device, error) {
	entries, err := os.ReadDir(b.SysClassBlock)
	if err != nil {
		return nil, err
	}
	mounts, err := parseMountInfo(b.MountInfo)
	if err != nil {
		return nil, err
	}
	swaps, err := parseSwaps(b.Swaps)
	if err != nil {
		return nil, err
	}
	critical := map[string]bool{"/": true, "/boot": true, "/boot/efi": true, "/home": true}
	var result []device.Device
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		name := entry.Name()
		link := filepath.Join(b.SysClassBlock, name)
		if exists(filepath.Join(link, "partition")) {
			continue
		}
		real, err := filepath.EvalSymlinks(link)
		if err != nil {
			continue
		}
		maj, min, err := readDeviceNumber(filepath.Join(link, "dev"))
		if err != nil {
			continue
		}
		props := b.udev(ctx, filepath.Join(b.DevRoot, name))
		d := device.Device{Path: filepath.Join(b.DevRoot, name), Major: maj, Minor: min, SysfsPath: real, Vendor: readTrim(filepath.Join(link, "device/vendor")), Model: readTrim(filepath.Join(link, "device/model")), Serial: first(props["ID_SERIAL_SHORT"], readTrim(filepath.Join(link, "device/serial"))), WWN: props["ID_WWN"], Transport: strings.ToLower(props["ID_BUS"])}
		d.Size = readUint(filepath.Join(link, "size")) * 512
		d.PartitionCount = countPartitions(entries, name, b.SysClassBlock)
		d.MountPoints = appendUnique(d.MountPoints, mounts[devNumber{maj, min}]...)
		for key, points := range mounts {
			if parentName(b.SysClassBlock, key) == name {
				d.MountPoints = appendUnique(d.MountPoints, points...)
			}
		}
		d.Mounted = len(d.MountPoints) > 0
		for _, p := range d.MountPoints {
			if critical[p] {
				d.IsSystemDisk = true
			}
		}
		for path := range swaps {
			if filepath.Base(path) == name || strings.HasPrefix(parentForPath(b.SysClassBlock, path), name) {
				d.IsSystemDisk = true
			}
		}
		d.IsCardReader = isCardReader(props)
		usb := d.Transport == "usb" && strings.Contains(real, "/usb")
		removable := readTrim(filepath.Join(link, "removable")) == "1"
		diskType := readTrim(filepath.Join(link, "device/type")) == "0"
		isFlash := props["ID_DRIVE_THUMB"] == "1" || props["ID_DRIVE_FLASH"] == "1"
		isSmallUSBStorage := props["ID_USB_DRIVER"] == "usb-storage" && d.Size <= maxGenericUSBFlashSize
		suspicious := containsAny(strings.ToLower(strings.Join([]string{d.Vendor, d.Model, props["ID_MODEL"], props["ID_VENDOR"]}, " ")), "ssd", "hard disk", "hdd", "bridge", "sata", "nvme") || props["ID_ATA"] == "1" || props["ID_USB_DRIVER"] == "uas"
		switch {
		case d.IsSystemDisk:
			d.RejectReason = ErrSystemDisk.Error()
		case !usb || !removable || !diskType || d.Size == 0 || suspicious:
			d.RejectReason = ErrUnsupportedDevice.Error()
		case !isFlash && !d.IsCardReader && !isSmallUSBStorage:
			d.RejectReason = "not positively identified as USB flash media"
		default:
			d.IsAllowed = true
		}
		d.ID = first(d.Serial, d.WWN, fmt.Sprintf("%d:%d@%s", maj, min, real))
		result = append(result, d)
	}
	return result, nil
}

func (b *Backend) RefreshDevice(ctx context.Context, id string) (device.Device, error) {
	all, err := b.list(ctx)
	if err != nil {
		return device.Device{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return device.Device{}, os.ErrNotExist
}
func (b *Backend) Revalidate(ctx context.Context, selected device.Device) (device.Device, error) {
	fresh, err := b.RefreshDevice(ctx, selected.ID)
	if err != nil {
		return device.Device{}, fmt.Errorf("%w: %w", ErrDeviceChanged, err)
	}
	if !fresh.IsAllowed {
		return device.Device{}, fmt.Errorf("%w: %s", ErrUnsupportedDevice, fresh.RejectReason)
	}
	if !device.SameIdentity(selected, fresh) {
		return device.Device{}, ErrDeviceChanged
	}
	return fresh, nil
}

func (b *Backend) Unmount(ctx context.Context, d device.Device) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	for _, partition := range b.mountedPartitions(fresh) {
		if out, err := b.runner.Output(ctx, "udisksctl", "unmount", "--block-device", partition); err != nil {
			return fmt.Errorf("%w: %s: %w: %s", ErrUnmountFailed, partition, err, strings.TrimSpace(string(out)))
		}
	}
	again, err := b.Revalidate(ctx, fresh)
	if err != nil {
		return err
	}
	if again.Mounted {
		return fmt.Errorf("%w: device remains mounted", ErrUnmountFailed)
	}
	return nil
}

func (b *Backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return nil, err
	}
	if fresh.Mounted {
		return nil, ErrUnmountFailed
	}
	return b.privileged().OpenWriter(ctx, helperRequest(fresh, modeWrite))
}
func (b *Backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return nil, err
	}
	if fresh.Mounted {
		return nil, ErrUnmountFailed
	}
	return b.privileged().OpenReader(ctx, helperRequest(fresh, modeRead))
}
func (b *Backend) Flush(ctx context.Context, d device.Device) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	if fresh.Mounted {
		return ErrUnmountFailed
	}
	return b.privileged().Flush(ctx, helperRequest(fresh, modeFlush))
}

func (b *Backend) privileged() privilegedHelper {
	if b.helper == nil {
		b.helper = newCommandHelper()
	}
	return b.helper
}
func (b *Backend) Eject(ctx context.Context, d device.Device) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	_, err = b.runner.Output(ctx, "udisksctl", "power-off", "--block-device", fresh.Path)
	return err
}

// FormatFAT32 creates a FAT32 filesystem on the whole removable device through
// the narrowly scoped privileged helper; revalidation and unmounting happen
// first.
func (b *Backend) FormatFAT32(ctx context.Context, d device.Device, label string) error {
	fresh, err := b.Revalidate(ctx, d)
	if err != nil {
		return err
	}
	if err := b.Unmount(ctx, fresh); err != nil {
		return err
	}
	again, err := b.Revalidate(ctx, fresh)
	if err != nil {
		return err
	}
	request := helperRequest(again, modeFormatFAT32)
	request.Label = label
	if err := b.privileged().FormatFAT32(ctx, request); err != nil {
		return fmt.Errorf("format FAT32: %w", err)
	}
	return nil
}

type devNumber struct{ major, minor uint32 }

func parseMountInfo(path string) (map[devNumber][]string, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := map[devNumber][]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		p := strings.Fields(s.Text())
		if len(p) < 5 {
			continue
		}
		a := strings.Split(p[2], ":")
		if len(a) != 2 {
			continue
		}
		ma, majorErr := strconv.ParseUint(a[0], 10, 32)
		mi, minorErr := strconv.ParseUint(a[1], 10, 32)
		if majorErr != nil || minorErr != nil {
			continue
		}
		out[devNumber{uint32(ma), uint32(mi)}] = append(out[devNumber{uint32(ma), uint32(mi)}], decodeMount(p[4]))
	}
	return out, s.Err()
}
func parseSwaps(path string) (map[string]bool, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	out := map[string]bool{}
	s := bufio.NewScanner(f)
	firstLine := true
	for s.Scan() {
		if firstLine {
			firstLine = false
			continue
		}
		p := strings.Fields(s.Text())
		if len(p) > 0 {
			out[p[0]] = true
		}
	}
	return out, s.Err()
}
func decodeMount(s string) string {
	r := strings.NewReplacer("\\040", " ", "\\011", "\t", "\\012", "\n", "\\134", "\\")
	return r.Replace(s)
}
func readDeviceNumber(path string) (uint32, uint32, error) {
	p := strings.Split(readTrim(path), ":")
	if len(p) != 2 {
		return 0, 0, errors.New("bad device number")
	}
	a, e := strconv.ParseUint(p[0], 10, 32)
	if e != nil {
		return 0, 0, e
	}
	c, e := strconv.ParseUint(p[1], 10, 32)
	return uint32(a), uint32(c), e
}
func readTrim(p string) string { v, _ := os.ReadFile(p); return strings.TrimSpace(string(v)) }
func readUint(p string) uint64 { v, _ := strconv.ParseUint(readTrim(p), 10, 64); return v }
func exists(p string) bool     { _, e := os.Stat(p); return e == nil }
func first(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
func appendUnique(dst []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
}
func isCardReader(p map[string]string) bool {
	for _, k := range []string{"ID_DRIVE_FLASH_SD", "ID_DRIVE_FLASH_MMC", "ID_DRIVE_FLASH_CF", "ID_DRIVE_FLASH_MS", "ID_DRIVE_FLASH_SM"} {
		if p[k] == "1" {
			return true
		}
	}
	return false
}
func countPartitions(entries []os.DirEntry, name, root string) int {
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), name) && exists(filepath.Join(root, e.Name(), "partition")) {
			n++
		}
	}
	return n
}
func parentName(root string, d devNumber) string {
	for _, e := range mustReadDir(root) {
		if readTrim(filepath.Join(root, e.Name(), "dev")) == fmt.Sprintf("%d:%d", d.major, d.minor) {
			return parentForPath(root, e.Name())
		}
	}
	return ""
}
func parentForPath(root, path string) string {
	name := filepath.Base(path)
	real, e := filepath.EvalSymlinks(filepath.Join(root, name))
	if e != nil {
		return name
	}
	if exists(filepath.Join(root, name, "partition")) {
		return filepath.Base(filepath.Dir(real))
	}
	return name
}
func mustReadDir(p string) []os.DirEntry { e, _ := os.ReadDir(p); return e }
func (b *Backend) mountedPartitions(parent device.Device) []string {
	mounts, _ := parseMountInfo(b.MountInfo)
	var result []string
	for number := range mounts {
		for _, entry := range mustReadDir(b.SysClassBlock) {
			if readTrim(filepath.Join(b.SysClassBlock, entry.Name(), "dev")) == fmt.Sprintf("%d:%d", number.major, number.minor) && parentForPath(b.SysClassBlock, entry.Name()) == filepath.Base(parent.Path) {
				result = append(result, filepath.Join(b.DevRoot, entry.Name()))
			}
		}
	}
	return result
}
func (b *Backend) udev(ctx context.Context, path string) map[string]string {
	out, e := b.runner.Output(ctx, "udevadm", "info", "--query=property", "--name", path)
	if e != nil {
		return map[string]string{}
	}
	p := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			p[k] = v
		}
	}
	return p
}
