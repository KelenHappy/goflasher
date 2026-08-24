//go:build linux

package linux

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/fat32"
	"github.com/goflasher/goflasher/internal/privilege"
	"github.com/goflasher/goflasher/internal/progress"
	"golang.org/x/sys/unix"
)

const helperExecutable = "/usr/libexec/goflasher-helper"
const embeddedHelperArgument = "--goflasher-privileged-helper"

var ErrAuthorizationCanceled = errors.New("authorization canceled")

type operationMode string

const (
	modeWrite            operationMode = "write"
	modeRead             operationMode = "read-back"
	modeFlush            operationMode = "flush"
	modeFormatFAT32      operationMode = "format-fat32"
	modeInstallerSession operationMode = "installer-session"
)

// privilegedRequest deliberately has no device-path field. The privileged side
// derives both sysfs and /dev names from the expected kernel device number.
type privilegedRequest struct {
	Version           uint32        `json:"version"`
	Identity          string        `json:"identity"`
	Serial            string        `json:"serial,omitempty"`
	WWN               string        `json:"wwn,omitempty"`
	Major             uint32        `json:"major"`
	Minor             uint32        `json:"minor"`
	Capacity          uint64        `json:"capacity"`
	Mode              operationMode `json:"mode"`
	Label             string        `json:"label,omitempty"`
	LogicalSectorSize uint32        `json:"logical_sector_size,omitempty"`
}

func helperRequest(d device.Device, mode operationMode) privilegedRequest {
	return privilegedRequest{Version: privilege.ProtocolVersion, Identity: d.ID, Serial: d.Serial, WWN: d.WWN, Major: d.Major, Minor: d.Minor, Capacity: d.Size, Mode: mode}
}

type privilegedHelper interface {
	OpenWriter(context.Context, privilegedRequest) (io.WriteCloser, error)
	OpenReader(context.Context, privilegedRequest) (io.ReadCloser, error)
	Flush(context.Context, privilegedRequest) error
	FormatFAT32(context.Context, privilegedRequest, chan<- progress.Update) error
}
type installerSessionHelper interface {
	OpenInstallerSession(context.Context, privilegedRequest) (*remoteInstallerSession, error)
}

type commandHelper struct {
	executable string
	arguments  []string
}

func newCommandHelper() privilegedHelper {
	executable := currentExecutable()
	// Only existence and an exec bit are checked, not a hash or signature.
	// App-level integrity verification here would be theater: the helper lives at
	// a root-owned path, so an attacker able to replace it is already root and
	// could equally patch any expected-hash baked into this unprivileged binary.
	// Integrity is instead owned by the package manager (rpm -V / dpkg --verify)
	// and, on macOS, codesign of the separately signed helper.
	for _, candidate := range helperCandidates(executable) {
		if executableFile(candidate) {
			return &commandHelper{executable: candidate}
		}
	}
	// Last-resort fallback: re-exec this binary as the privileged helper. This
	// runs the whole GUI binary as root under pkexec, a larger attack surface
	// than the dedicated helper (dispatchEmbeddedHelper returns before any GUI
	// init, but imported packages' init() still run as root). Every packaged
	// build (deb/rpm/arch) ships the standalone cmd/goflasher-helper at
	// /usr/libexec, tried first above, so this path is reached only by a bare GUI
	// binary with no accompanying helper — not by any shipped artifact.
	return &commandHelper{executable: executable, arguments: []string{embeddedHelperArgument}}
}

func currentExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return helperExecutable
	}
	return executable
}

func executableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0111 != 0
}

// helperCandidates picks which binary the GUI hands to pkexec. This selection
// is a convenience/packaging concern, NOT a security boundary. It runs entirely
// in the unprivileged GUI before pkexec, so the executable-relative candidate
// grants no capability an attacker who already controls this process lacks —
// such an attacker could invoke pkexec directly. The privilege boundary is the
// root helper, which trusts nothing in the request: it re-derives the device
// from the kernel major/minor and rebinds via fstat(fd) in
// validateOpenedDevice. The fixed /usr/libexec path is tried first, so on a
// normal install the relative candidate is never reached.
func helperCandidates(executable string) []string {
	candidates := []string{helperExecutable}
	if executable != "" {
		candidates = appendHelperCandidate(candidates, filepath.Clean(filepath.Join(filepath.Dir(executable), "..", "libexec", "goflasher-helper")))
	}
	return candidates
}

func appendHelperCandidate(candidates []string, candidate string) []string {
	for _, existing := range candidates {
		if existing == candidate {
			return candidates
		}
	}
	return append(candidates, candidate)
}

// IsEmbeddedHelperInvocation reports whether this process was started by the
// GUI through pkexec to serve one privileged operation.
func IsEmbeddedHelperInvocation(args []string) bool {
	return len(args) == 2 && args[1] == embeddedHelperArgument
}

func (h *commandHelper) start(ctx context.Context, r privilegedRequest, updates chan<- progress.Update) (*exec.Cmd, io.WriteCloser, io.ReadCloser, *strings.Builder, error) {
	pkexecArgs := append([]string{h.executable}, h.arguments...)
	cmd := exec.CommandContext(ctx, "pkexec", pkexecArgs...)
	in, out, err := commandPipes(cmd)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stderr := &strings.Builder{}
	if updates != nil {
		cmd.Stderr = &progressParser{builder: stderr, updates: updates}
	} else {
		cmd.Stderr = stderr
	}
	// Starting pkexec is intentionally synchronous: authorization must finish
	// before any privileged request bytes are sent, and each helper is a
	// security-scoped, one-operation process rather than a reusable worker.
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

type progressParser struct {
	builder *strings.Builder
	updates chan<- progress.Update
	buf     []byte
}

func (p *progressParser) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for line, ok := p.nextLine(); ok; line, ok = p.nextLine() {
		p.writeLine(line)
	}
	return len(b), nil
}

func (p *progressParser) nextLine() (string, bool) {
	idx := bytes.IndexByte(p.buf, '\n')
	if idx < 0 {
		return "", false
	}
	line := string(p.buf[:idx])
	p.buf = p.buf[idx+1:]
	return line, true
}

func (p *progressParser) writeLine(line string) {
	if update, ok := formattingProgress(line); ok {
		if p.updates != nil {
			p.updates <- update
		}
		return
	}
	p.builder.WriteString(line)
	p.builder.WriteByte('\n')
}

func formattingProgress(line string) (progress.Update, bool) {
	if !strings.HasPrefix(line, "PROGRESS ") {
		return progress.Update{}, false
	}
	var processed, total uint64
	if n, _ := fmt.Sscanf(line, "PROGRESS %d %d", &processed, &total); n != 2 {
		return progress.Update{}, false
	}
	return progress.Calculate(progress.StageFormatting, processed, total, 0), true
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
	cmd, in, out, stderr, err := h.start(ctx, r, nil)
	if out != nil {
		_ = out.Close()
	}
	if err != nil {
		return nil, err
	}
	return &processWriter{WriteCloser: in, cmd: cmd, stderr: stderr}, nil
}
func (h *commandHelper) OpenReader(ctx context.Context, r privilegedRequest) (io.ReadCloser, error) {
	cmd, in, out, _, err := h.start(ctx, r, nil)
	if err != nil {
		return nil, err
	}
	_ = in.Close()
	return &processReader{ReadCloser: out, input: in, cmd: cmd}, nil
}
func (h *commandHelper) Flush(ctx context.Context, r privilegedRequest) error {
	cmd, in, out, _, err := h.start(ctx, r, nil)
	if err != nil {
		return err
	}
	_ = in.Close()
	_ = out.Close()
	return cmd.Wait()
}

func (h *commandHelper) FormatFAT32(ctx context.Context, r privilegedRequest, updates chan<- progress.Update) error {
	cmd, in, out, stderr, err := h.start(ctx, r, updates)
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

// Installer architecture decision: GPT/FAT construction stays in the
// unprivileged process. The root helper exposes only this identity-bound,
// capacity-bounded random-access operation stream; it never accepts a device
// pathname, filesystem pathname, or arbitrary command to execute.
type remoteInstallerSession struct {
	mu     sync.Mutex
	ctx    context.Context
	input  io.WriteCloser
	output *bufio.Reader
	closer io.Closer
	cmd    *exec.Cmd
	closed bool
}

func (h *commandHelper) OpenInstallerSession(ctx context.Context, r privilegedRequest) (*remoteInstallerSession, error) {
	r.Mode, r.Version, r.LogicalSectorSize = modeInstallerSession, privilege.ProtocolVersion, 512
	cmd, in, out, _, err := h.start(ctx, r, nil)
	if err != nil {
		return nil, err
	}
	reader, ok := out.(*bufferedReadCloser)
	if !ok {
		_ = in.Close()
		_ = out.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("invalid helper session stream")
	}
	buffered, ok := reader.Reader.(*bufio.Reader)
	if !ok {
		_ = in.Close()
		_ = out.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, errors.New("invalid buffered helper stream")
	}
	return &remoteInstallerSession{ctx: ctx, input: in, output: buffered, closer: out, cmd: cmd}, nil
}

func (s *remoteInstallerSession) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || len(p) == 0 {
		return 0, privilege.ErrInvalidTarget
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	written := 0
	for written < len(p) {
		n := min(len(p)-written, int(maxSessionTransfer))
		if err := s.command(privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionWriteAt, Offset: uint64(off) + uint64(written), Length: uint32(n)}, p[written:written+n], nil); err != nil {
			return written, err
		}
		written += n
	}
	return written, nil
}
func (s *remoteInstallerSession) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || len(p) == 0 {
		return 0, privilege.ErrInvalidTarget
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	read := 0
	for read < len(p) {
		n := min(len(p)-read, int(maxSessionTransfer))
		if err := s.command(privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionReadAt, Offset: uint64(off) + uint64(read), Length: uint32(n)}, nil, p[read:read+n]); err != nil {
			return read, err
		}
		read += n
	}
	return read, nil
}
func (s *remoteInstallerSession) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.command(privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionFlush}, nil, nil)
}
func (s *remoteInstallerSession) command(c privilege.SessionCommand, write, read []byte) error {
	if s.closed {
		return os.ErrClosed
	}
	if err := s.ctx.Err(); err != nil {
		return err
	}
	data, _ := json.Marshal(c)
	data = append(data, '\n')
	if _, err := s.input.Write(data); err != nil {
		return err
	}
	if err := writeSessionPayload(s.input, write); err != nil {
		return err
	}
	return s.readCommandResponse(read)
}

func writeSessionPayload(output io.Writer, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	_, err := output.Write(payload)
	return err
}

func (s *remoteInstallerSession) readCommandResponse(read []byte) error {
	line, err := s.output.ReadBytes('\n')
	if err != nil {
		return err
	}
	var response privilege.SessionResponse
	if err = json.Unmarshal(line, &response); err != nil {
		return err
	}
	if response.Version != privilege.ProtocolVersion {
		return privilege.ErrIncompatible
	}
	if !response.OK {
		return &privilege.SessionError{Code: response.Code, Message: response.Message}
	}
	if len(read) == 0 {
		return nil
	}
	if response.Length != uint32(len(read)) {
		return io.ErrUnexpectedEOF
	}
	_, err = io.ReadFull(s.output, read)
	return err
}
func (s *remoteInstallerSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	var commandErr error
	if s.ctx.Err() != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	} else {
		commandErr = s.command(privilege.SessionCommand{Version: privilege.ProtocolVersion, Kind: privilege.SessionClose}, nil, nil)
	}
	s.closed = true
	_ = s.input.Close()
	_ = s.closer.Close()
	return errors.Join(commandErr, s.cmd.Wait())
}

type helperEnvironment struct {
	SysDevBlock, SysClassBlock, MountInfo, Swaps, DevRoot string
	openFile                                              func(string, int, os.FileMode) (*os.File, error)
	fstat                                                 func(int, *syscall.Stat_t) error
}

func realHelperEnvironment() helperEnvironment {
	return helperEnvironment{SysDevBlock: "/sys/dev/block", SysClassBlock: "/sys/class/block", MountInfo: "/proc/self/mountinfo", Swaps: "/proc/swaps", DevRoot: "/dev"}
}

// RunPrivilegedHelper serves exactly one authenticated operation over stdin/stdout.
func RunPrivilegedHelper(in io.Reader, out io.Writer, errOut io.Writer) error {
	return runPrivilegedHelper(in, out, errOut, realHelperEnvironment())
}

func runPrivilegedHelper(in io.Reader, out io.Writer, errOut io.Writer, env helperEnvironment) error {
	req, payload, err := readPrivilegedRequest(in)
	if err != nil {
		return err
	}
	f, err := validateAndOpen(req, env)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = io.WriteString(out, "OK\n"); err != nil {
		return err
	}
	operation := privilegedOperation{request: req, target: f, payload: payload, output: out, errorOutput: errOut, env: env}
	return operation.execute()
}

type privilegedOperation struct {
	request     privilegedRequest
	target      *os.File
	payload     io.Reader
	output      io.Writer
	errorOutput io.Writer
	env         helperEnvironment
}

func (o privilegedOperation) execute() error {
	switch o.request.Mode {
	case modeWrite:
		return writeAndSync(o.target, o.payload)
	case modeRead:
		_, err := io.CopyN(o.output, o.target, int64(o.request.Capacity))
		return err
	case modeFlush:
		return flushAndInvalidate(o.target, func() error { return invalidateBlockCache(o.target) })
	case modeFormatFAT32:
		return makeFAT32(o.target, o.request.Capacity, o.request.Label, o.errorOutput)
	case modeInstallerSession:
		return runInstallerSession(installerSession{request: o.request, target: o.target, input: bufio.NewReader(o.payload), output: o.output, env: o.env})
	default:
		return errors.New("unsupported operation mode")
	}
}

const maxPrivilegedRequestBytes = 4096

// readPrivilegedRequest bounds JSON decoding without adding a frame delimiter.
// Delimiter-free requests remain compatible with older installed helpers,
// which treat every byte after the JSON object as image payload. Any bytes the
// decoder reads ahead are put back in front of the remaining payload.
func readPrivilegedRequest(in io.Reader) (privilegedRequest, io.Reader, error) {
	limited := &io.LimitedReader{R: in, N: maxPrivilegedRequestBytes}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var req privilegedRequest
	if err := decoder.Decode(&req); err != nil {
		return privilegedRequest{}, nil, fmt.Errorf("invalid request: %w", err)
	}
	payload := io.MultiReader(decoder.Buffered(), limited, in)
	return req, payload, nil
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
}, payload io.Reader) error {
	if _, err := io.Copy(target, payload); err != nil {
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
	if !req.validProtocol() || !req.hasRequiredIdentity() {
		return false
	}
	return req.validMode()
}

func (req privilegedRequest) hasRequiredIdentity() bool {
	return req.Identity != "" && req.Capacity != 0
}

func (req privilegedRequest) validMode() bool {
	switch req.Mode {
	case modeWrite, modeRead, modeFlush, modeInstallerSession:
		return true
	case modeFormatFAT32:
		return fat32.ValidLabel(req.Label)
	default:
		return false
	}
}

func (req privilegedRequest) validProtocol() bool {
	if req.Mode != modeInstallerSession {
		return req.Version == 0 || req.Version == privilege.ProtocolVersion
	}
	return req.Version == privilege.ProtocolVersion && validLogicalSectorSize(req.LogicalSectorSize)
}

func validLogicalSectorSize(size uint32) bool {
	return size >= 512 && size&(size-1) == 0
}

const maxSessionTransfer = uint32(4 << 20)

type installerSession struct {
	request privilegedRequest
	target  *os.File
	input   *bufio.Reader
	output  io.Writer
	env     helperEnvironment
}

func runInstallerSession(session installerSession) error {
	return session.run()
}

func (s *installerSession) run() error {
	for {
		command, ok, err := s.readCommand()
		if !ok {
			return err
		}
		if command.Kind == privilege.SessionCancel || command.Kind == privilege.SessionClose {
			return s.reply(privilege.SessionResponse{OK: true})
		}
		if err := s.execute(command); err != nil {
			return err
		}
	}
}

func (s *installerSession) readCommand() (privilege.SessionCommand, bool, error) {
	line, err := s.input.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxPrivilegedRequestBytes {
		return privilege.SessionCommand{}, false, s.failure("invalid-command", "command frame too large")
	}
	if err != nil {
		return privilege.SessionCommand{}, false, err
	}
	var command privilege.SessionCommand
	if err := json.Unmarshal(line, &command); err != nil {
		return command, false, s.failure("invalid-command", err.Error())
	}
	if err := command.Validate(s.request.Capacity); err != nil {
		return command, false, s.failure("invalid-command", err.Error())
	}
	if command.Length > maxSessionTransfer {
		return command, false, s.failure("transfer-too-large", "session transfer exceeds bound")
	}
	return command, true, nil
}

func (s *installerSession) execute(command privilege.SessionCommand) error {
	payload, err := s.readWritePayload(command)
	if err != nil {
		return err
	}
	if err := s.revalidateTarget(); err != nil {
		_ = s.failure("device-changed", err.Error())
		return err
	}
	if err := s.perform(command, payload); err != nil {
		_ = s.failure("operation-failed", err.Error())
		return err
	}
	return nil
}

func (s *installerSession) readWritePayload(command privilege.SessionCommand) ([]byte, error) {
	if command.Kind != privilege.SessionWriteAt {
		return nil, nil
	}
	payload := make([]byte, command.Length)
	_, err := io.ReadFull(s.input, payload)
	return payload, err
}

func (s *installerSession) revalidateTarget() error {
	name, _, err := resolveDevice(s.request, s.env)
	if err != nil {
		return err
	}
	return validateOpenedDevice(s.request, s.env, name, s.target)
}

func (s *installerSession) perform(command privilege.SessionCommand, payload []byte) error {
	switch command.Kind {
	case privilege.SessionWriteAt:
		return s.performWrite(command, payload)
	case privilege.SessionReadAt:
		return s.performRead(command)
	case privilege.SessionFlush:
		return s.performFlush()
	default:
		return s.failure("invalid-command", "unsupported session command")
	}
}

func (s *installerSession) performWrite(command privilege.SessionCommand, payload []byte) error {
	if _, err := s.target.WriteAt(payload, int64(command.Offset)); err != nil {
		return err
	}
	return s.reply(privilege.SessionResponse{OK: true})
}

func (s *installerSession) performRead(command privilege.SessionCommand) error {
	payload := make([]byte, command.Length)
	if _, err := s.target.ReadAt(payload, int64(command.Offset)); err != nil {
		return err
	}
	if err := s.reply(privilege.SessionResponse{OK: true, Length: command.Length}); err != nil {
		return err
	}
	_, err := s.output.Write(payload)
	return err
}

func (s *installerSession) performFlush() error {
	if err := s.target.Sync(); err != nil {
		return err
	}
	return s.reply(privilege.SessionResponse{OK: true})
}

func (s *installerSession) failure(code, message string) error {
	return s.reply(privilege.SessionResponse{Code: code, Message: message})
}

func (s *installerSession) reply(response privilege.SessionResponse) error {
	response.Version = privilege.ProtocolVersion
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.output.Write(data)
	return err
}

func makeFAT32(device *os.File, size uint64, label string, errOut io.Writer) error {
	return fat32.Format(context.Background(), device, size, label, func(percent uint64) { fmt.Fprintf(errOut, "PROGRESS %d 100\n", percent) })
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
	if major != req.Major {
		return 0, 0, ErrDeviceChanged
	}
	if req.Mode == modeInstallerSession && readTrim(filepath.Join(class, "queue/logical_block_size")) != fmt.Sprint(req.LogicalSectorSize) {
		return 0, 0, ErrDeviceChanged
	}
	if minor != req.Minor {
		return 0, 0, ErrDeviceChanged
	}
	if readUint(filepath.Join(class, "size"))*512 != req.Capacity {
		return 0, 0, ErrDeviceChanged
	}
	if !readDeviceIdentities(class, real, major, minor).matches(req) {
		return 0, 0, ErrDeviceChanged
	}
	return major, minor, nil
}

type deviceIdentities struct {
	serials, wwns, all []string
}

func readDeviceIdentities(class, real string, major, minor uint32) deviceIdentities {
	serials := uniqueIdentityValues(readTrim(filepath.Join(class, "device/serial")), readUSBAncestorAttribute(real, "serial"))
	wwns := uniqueIdentityValues(readTrim(filepath.Join(class, "wwid")), readTrim(filepath.Join(class, "device/wwid")))
	all := append(append([]string{}, serials...), wwns...)
	all = append(all, fmt.Sprintf("%d:%d@%s", major, minor, real))
	return deviceIdentities{serials: serials, wwns: wwns, all: all}
}

func (ids deviceIdentities) matches(req privilegedRequest) bool {
	if req.Serial != "" && !containsIdentity(ids.serials, req.Serial) {
		return false
	}
	if req.WWN != "" && !containsIdentity(ids.wwns, req.WWN) {
		return false
	}
	return containsIdentity(ids.all, req.Identity)
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
	path := usbDeviceAncestor(real)
	if path == "" || !exists(filepath.Join(path, "idVendor")) || !exists(filepath.Join(path, "idProduct")) {
		return ""
	}
	return readTrim(filepath.Join(path, attribute))
}

// usbDeviceAncestor uses the kernel's documented USB sysfs node names to find
// the nearest physical device. Interface nodes contain a colon (for example,
// 1-13:1.0); device nodes contain a bus-port chain such as 1-13 or 1-2.4.
// Keeping this ancestry walk lexical avoids probing the filesystem at every
// level of the SCSI/block-device path.
func usbDeviceAncestor(real string) string {
	for path := filepath.Clean(real); path != filepath.Dir(path); path = filepath.Dir(path) {
		if isUSBDeviceNode(filepath.Base(path)) {
			return path
		}
	}
	return ""
}

func isUSBDeviceNode(name string) bool {
	bus, ports, found := strings.Cut(name, "-")
	if !found || !decimalDigits(bus) || ports == "" {
		return false
	}
	for _, port := range strings.Split(ports, ".") {
		if !decimalDigits(port) {
			return false
		}
	}
	return true
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
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

// validateDeviceNode is a best-effort pre-open fail-fast check, NOT a security
// boundary. It stats a path, so a TOCTOU swap of /dev/<name> between this Stat
// and the subsequent open() is possible. That window is harmless because the
// authoritative decision runs post-open in validateOpenedDevice via fstat(fd),
// which rebinds to the actual kernel object before any write or ioctl. Do not
// rely on this function alone for authorization.
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
	flags := deviceOpenFlags(req.Mode)
	opener := env.openFile
	if opener == nil {
		opener = os.OpenFile
	}
	f, err := opener(filepath.Join(env.DevRoot, name), flags|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	if err := validateOpenedDevice(req, env, name, f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func deviceOpenFlags(mode operationMode) int {
	switch mode {
	case modeFormatFAT32:
		// The in-process formatter uses random-access writes and syncs the
		// completed filesystem, so its validated descriptor must be read/write.
		return os.O_RDWR
	case modeInstallerSession:
		return os.O_RDWR | syscall.O_SYNC
	case modeWrite:
		// Keep each large stream write tied to actual device progress instead of
		// letting Linux accept the entire image into the page cache at RAM speed.
		// Besides producing meaningful throughput/ETA figures, this bounds the
		// otherwise very long and apparently frozen Sync at the end of a write.
		return os.O_WRONLY | syscall.O_SYNC
	case modeRead:
		return os.O_RDONLY
	default:
		return os.O_WRONLY
	}
}

// validateOpenedDevice binds the final authorization decision to the kernel
// object referenced by f. Opening a raw device does not itself write bytes, so
// every destructive mode reaches this check before its first write or ioctl.
func validateOpenedDevice(req privilegedRequest, env helperEnvironment, expectedName string, f *os.File) error {
	if err := validateOpenedDescriptor(req, env, f); err != nil {
		return err
	}
	name, err := revalidateResolvedDevice(req, env, expectedName)
	if err != nil {
		return err
	}
	return revalidateDeviceSafety(req, env, name)
}

func validateOpenedDescriptor(req privilegedRequest, env helperEnvironment, f *os.File) error {
	var st syscall.Stat_t
	fstat := env.fstat
	if fstat == nil {
		fstat = syscall.Fstat
	}
	if err := fstat(int(f.Fd()), &st); err != nil {
		return fmt.Errorf("fstat opened device: %w", err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFBLK {
		return fmt.Errorf("%w: opened descriptor is not requested block device", ErrDeviceChanged)
	}
	if unix.Major(uint64(st.Rdev)) != req.Major {
		return fmt.Errorf("%w: opened descriptor is not requested block device", ErrDeviceChanged)
	}
	if unix.Minor(uint64(st.Rdev)) != req.Minor {
		return fmt.Errorf("%w: opened descriptor is not requested block device", ErrDeviceChanged)
	}
	return nil
}

func revalidateResolvedDevice(req privilegedRequest, env helperEnvironment, expectedName string) (string, error) {
	name, real, err := resolveDevice(req, env)
	if err != nil {
		return "", fmt.Errorf("%w: opened device no longer resolves", ErrDeviceChanged)
	}
	if name != expectedName {
		return "", fmt.Errorf("%w: opened device no longer resolves", ErrDeviceChanged)
	}
	if _, _, err := validateDeviceMetadata(req, filepath.Join(env.SysClassBlock, name), real); err != nil {
		return "", fmt.Errorf("revalidate opened device metadata: %w", err)
	}
	return name, nil
}

func revalidateDeviceSafety(req privilegedRequest, env helperEnvironment, name string) error {
	// Mount and swap state may also have changed while open() was pending.
	unsafe, err := mountedOrSystem(name, req.Major, req.Minor, env)
	if err != nil {
		return fmt.Errorf("revalidate opened device safety: %w", err)
	}
	if unsafe {
		return ErrSystemDisk
	}
	return nil
}

func mountedOrSystem(name string, major, minor uint32, env helperEnvironment) (bool, error) {
	topology, err := readBlockTopology(env.SysClassBlock)
	if err != nil {
		return false, err
	}
	mounts, err := parseMountInfo(env.MountInfo)
	if err != nil {
		return false, err
	}
	if mounted, err := deviceMounted(name, major, minor, mounts, topology); err != nil {
		return false, err
	} else if mounted {
		return true, nil
	}
	swaps, err := parseSwaps(env.Swaps)
	if err != nil {
		return false, err
	}
	return deviceUsedForSwap(name, env.DevRoot, swaps, topology)
}

func deviceMounted(name string, major, minor uint32, mounts map[devNumber][]string, topology *blockTopology) (bool, error) {
	target := devNumber{major: major, minor: minor}
	for number, points := range mounts {
		if len(points) == 0 {
			continue
		}
		mounted, err := mountBacksDevice(number, target, name, topology)
		if err != nil {
			return false, err
		}
		if mounted {
			return true, nil
		}
	}
	return false, nil
}

func mountBacksDevice(number, target devNumber, name string, topology *blockTopology) (bool, error) {
	mounted, usable, err := mountedDeviceName(number, topology)
	if err != nil || !usable {
		return false, err
	}
	if number == target {
		return true, nil
	}
	return topology.dependsOn(mounted, name)
}

func mountedDeviceName(number devNumber, topology *blockTopology) (string, bool, error) {
	name, err := topology.nameForNumber(number)
	if err == nil {
		return name, true, nil
	}
	if number.major == 0 {
		return "", false, nil
	}
	return "", false, err
}

func deviceUsedForSwap(name, devRoot string, swaps map[string]bool, topology *blockTopology) (bool, error) {
	for path := range swaps {
		swapName, block, err := topology.nameForSwapPath(path, devRoot)
		if err != nil {
			return false, err
		}
		if !block {
			continue
		}
		backed, err := topology.dependsOn(swapName, name)
		if err != nil {
			return false, err
		}
		if backed {
			return true, nil
		}
	}
	return false, nil
}
