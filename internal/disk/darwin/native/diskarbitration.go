//go:build darwin

package native

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

type daAPI struct {
	sessionCreate       func(uintptr) uintptr
	diskCreateBSD       func(uintptr, uintptr, *byte) uintptr
	diskCopyDescription func(uintptr) uintptr
	diskGetBSDName      func(uintptr) *byte
	registerAppeared    func(uintptr, uintptr, uintptr, uintptr)
	unregisterCallback  func(uintptr, uintptr, uintptr)
	scheduleRunLoop     func(uintptr, uintptr, uintptr)
	unscheduleRunLoop   func(uintptr, uintptr, uintptr)
	diskUnmount         func(uintptr, uint32, uintptr, uintptr)
	diskEject           func(uintptr, uint32, uintptr, uintptr)
	dissenterStatus     func(uintptr) int32
}
type daBindings struct {
	api  daAPI
	keys map[string]uintptr
}

func bindDA(lib uintptr) (daBindings, error) {
	var d daBindings
	purego.RegisterLibFunc(&d.api.sessionCreate, lib, "DASessionCreate")
	purego.RegisterLibFunc(&d.api.diskCreateBSD, lib, "DADiskCreateFromBSDName")
	purego.RegisterLibFunc(&d.api.diskCopyDescription, lib, "DADiskCopyDescription")
	purego.RegisterLibFunc(&d.api.diskGetBSDName, lib, "DADiskGetBSDName")
	purego.RegisterLibFunc(&d.api.registerAppeared, lib, "DARegisterDiskAppearedCallback")
	purego.RegisterLibFunc(&d.api.unregisterCallback, lib, "DAUnregisterCallback")
	purego.RegisterLibFunc(&d.api.scheduleRunLoop, lib, "DASessionScheduleWithRunLoop")
	purego.RegisterLibFunc(&d.api.unscheduleRunLoop, lib, "DASessionUnscheduleFromRunLoop")
	purego.RegisterLibFunc(&d.api.diskUnmount, lib, "DADiskUnmount")
	purego.RegisterLibFunc(&d.api.diskEject, lib, "DADiskEject")
	purego.RegisterLibFunc(&d.api.dissenterStatus, lib, "DADissenterGetStatus")
	d.keys = map[string]uintptr{}
	for _, k := range []string{"kDADiskDescriptionMediaBSDNameKey", "kDADiskDescriptionMediaNameKey", "kDADiskDescriptionMediaSizeKey", "kDADiskDescriptionMediaWholeKey", "kDADiskDescriptionDeviceInternalKey", "kDADiskDescriptionMediaEjectableKey", "kDADiskDescriptionMediaRemovableKey", "kDADiskDescriptionVolumePathKey"} {
		v, e := symbolValue(lib, k)
		if e != nil {
			return d, fmt.Errorf("resolve %s: %w", k, e)
		}
		d.keys[k] = v
	}
	return d, nil
}

type Frameworks struct {
	libs libraries
	cf   cfBindings
	da   daBindings
	io   ioBindings
}

func OpenFrameworks() (*Frameworks, error) {
	l, e := loadLibraries()
	if e != nil {
		return nil, e
	}
	f := &Frameworks{libs: l}
	if f.cf, e = bindCF(l.cf); e != nil {
		return nil, e
	}
	if f.da, e = bindDA(l.da); e != nil {
		return nil, e
	}
	if f.io, e = bindIOKit(l.io, f.cf); e != nil {
		return nil, e
	}
	return f, nil
}
func (f *Frameworks) NewSession() (*Session, error) {
	r := f.da.api.sessionCreate(0)
	if r == 0 {
		return nil, errors.New("DASessionCreate returned NULL")
	}
	return &Session{f: f, ref: r}, nil
}

type Session struct {
	f          *Frameworks
	ref        uintptr
	diagnostic func(descriptionDiagnostics)
}

type valueDiagnostic struct {
	Found  bool
	TypeID uintptr
}

type descriptionDiagnostics struct {
	Disk              uintptr
	BSDName           string
	Description       uintptr
	DescriptionTypeID uintptr
	DictionaryTypeID  uintptr
	DictionaryCount   int64
	Values            map[string]valueDiagnostic
}

var errDiskWithoutBSDName = errors.New("DADiskGetBSDName and media BSD-name description were both empty")

func (s *Session) Close() {
	if s != nil && s.ref != 0 {
		s.f.cf.api.release(s.ref)
		s.ref = 0
	}
}
func (s *Session) DiskFromBSDName(name string) (DiskDescription, error) {
	if s == nil || s.ref == 0 {
		return DiskDescription{}, ErrUnavailable
	}
	z := append([]byte("/dev/"+name), 0)
	d := s.f.da.api.diskCreateBSD(0, s.ref, &z[0])
	if d == 0 {
		return DiskDescription{}, fmt.Errorf("DADiskCreateFromBSDName(%q) returned NULL", name)
	}
	defer s.f.cf.api.release(d)
	return s.describe(d)
}
func (s *Session) describe(d uintptr) (DiskDescription, error) {
	if d == 0 {
		return DiskDescription{}, errors.New("Disk Arbitration callback supplied NULL DADiskRef")
	}
	diagnostic := descriptionDiagnostics{Disk: d, Values: make(map[string]valueDiagnostic)}
	if p := s.f.da.api.diskGetBSDName(d); p != nil {
		diagnostic.BSDName = goCString(p)
	}
	x := s.f.da.api.diskCopyDescription(d)
	if x == 0 {
		s.emitDiagnostic(diagnostic)
		return DiskDescription{}, errors.New("DADiskCopyDescription returned NULL")
	}
	defer s.f.cf.api.release(x)
	diagnostic.Description = x
	diagnostic.DescriptionTypeID = s.f.cf.api.getTypeID(x)
	diagnostic.DictionaryTypeID = s.f.cf.api.dictionaryTypeID()
	if diagnostic.DescriptionTypeID != diagnostic.DictionaryTypeID {
		s.emitDiagnostic(diagnostic)
		return DiskDescription{}, fmt.Errorf("DADiskCopyDescription returned CFTypeID %d, want CFDictionary type %d", diagnostic.DescriptionTypeID, diagnostic.DictionaryTypeID)
	}
	diagnostic.DictionaryCount = s.f.cf.api.dictionaryGetCount(x)
	get := func(k string) uintptr {
		v := s.f.cf.dictionaryValue(x, s.f.da.keys[k])
		vd := valueDiagnostic{Found: v != 0}
		if v != 0 {
			vd.TypeID = s.f.cf.api.getTypeID(v)
		}
		diagnostic.Values[k] = vd
		return v
	}
	var out DiskDescription
	out.BSDName = diagnostic.BSDName
	if name, ok := s.f.cf.goString(get("kDADiskDescriptionMediaBSDNameKey")); ok {
		out.BSDName = name
	}
	out.MediaName, _ = s.f.cf.goString(get("kDADiskDescriptionMediaNameKey"))
	out.Size, _ = s.f.cf.goUint64(get("kDADiskDescriptionMediaSizeKey"))
	out.Whole, _ = s.f.cf.goBool(get("kDADiskDescriptionMediaWholeKey"))
	out.Internal, _ = s.f.cf.goBool(get("kDADiskDescriptionDeviceInternalKey"))
	out.Ejectable, _ = s.f.cf.goBool(get("kDADiskDescriptionMediaEjectableKey"))
	out.Removable, _ = s.f.cf.goBool(get("kDADiskDescriptionMediaRemovableKey"))
	out.VolumePath, _ = s.f.cf.goPath(get("kDADiskDescriptionVolumePathKey"))
	s.emitDiagnostic(diagnostic)
	if out.BSDName == "" {
		return DiskDescription{}, errDiskWithoutBSDName
	}
	return out, nil
}

func (s *Session) emitDiagnostic(d descriptionDiagnostics) {
	if s.diagnostic != nil {
		s.diagnostic(d)
	}
}

func goCString(p *byte) string {
	if p == nil {
		return ""
	}
	const maxCString = 4096
	b := unsafe.Slice(p, maxCString)
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return ""
}

// WaitForDisk proves that a Disk Arbitration C callback can safely enter Go.
// Native retains no Go pointer: the callback is a process-lifetime trampoline,
// while per-call state is referenced by a numeric token in callbackStates.
func (s *Session) WaitForDisk(ctx context.Context) (DiskDescription, error) {
	return waitForDisk(ctx, s)
}

// ListDisks collects the initial appeared-callback burst. Disk Arbitration
// delivers every currently known disk after registration; a quiet interval
// terminates the snapshot while the context provides the hard bound.
func (s *Session) ListDisks(ctx context.Context) ([]DiskDescription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	state := newCallbackState(s)
	defer state.close()
	stop := s.scheduleAppearedCallback(state.token)
	defer stop()
	collector := newDiskCollector()
	defer collector.close()
	for {
		if disks, err, done := collector.poll(ctx, state.result); done {
			return disks, err
		}
		s.pumpRunLoop()
	}
}

const diskListQuietInterval = 300 * time.Millisecond

type diskCollector struct {
	quiet  *time.Timer
	byName map[string]DiskDescription
}

func newDiskCollector() *diskCollector {
	return &diskCollector{quiet: time.NewTimer(diskListQuietInterval), byName: make(map[string]DiskDescription)}
}

func (c *diskCollector) close() { c.quiet.Stop() }

func (c *diskCollector) poll(ctx context.Context, results <-chan callbackResult) ([]DiskDescription, error, bool) {
	select {
	case result := <-results:
		if result.err != nil {
			return nil, result.err, true
		}
		c.byName[result.disk.BSDName] = result.disk
		resetTimer(c.quiet, diskListQuietInterval)
		return nil, nil, false
	case <-c.quiet.C:
		return diskDescriptions(c.byName), nil, true
	case <-ctx.Done():
		return nil, ctx.Err(), true
	default:
		return nil, nil, false
	}
}

func resetTimer(timer *time.Timer, interval time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(interval)
}

func diskDescriptions(byName map[string]DiskDescription) []DiskDescription {
	disks := make([]DiskDescription, 0, len(byName))
	for _, disk := range byName {
		disks = append(disks, disk)
	}
	return disks
}

func (s *Session) scheduleAppearedCallback(token uintptr) func() {
	loop := s.f.cf.api.runLoopGetCurrent()
	s.f.da.api.registerAppeared(s.ref, 0, appearedCallback, token)
	s.f.da.api.scheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	return func() {
		s.f.da.api.unregisterCallback(s.ref, appearedCallback, token)
		s.f.da.api.unscheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	}
}

func (s *Session) pumpRunLoop() {
	s.f.cf.api.runLoopRunInMode(s.f.cf.defaultRunLoopMode, 0.05, true)
	runtime.Gosched()
}

type DissenterError struct{ Status int32 }

func (e DissenterError) Error() string {
	return fmt.Sprintf("Disk Arbitration dissenter status %d", e.Status)
}

var operationCallback = purego.NewCallback(func(_ uintptr, dissenter, token uintptr) {
	v, ok := operationStates.LoadAndDelete(token)
	if !ok {
		return
	}
	state := v.(chan error)
	if dissenter == 0 {
		state <- nil
		return
	}
	// The frameworks pointer is held separately because callback ABI contexts
	// contain only an integer token, never a Go pointer.
	f, ok := operationFrameworks.LoadAndDelete(token)
	if !ok {
		state <- ErrUnavailable
		return
	}
	state <- DissenterError{Status: f.(*Frameworks).da.api.dissenterStatus(dissenter)}
})

var operationStates sync.Map
var operationFrameworks sync.Map
var nextOperationToken atomic.Uint64

func (s *Session) operation(ctx context.Context, bsd string, eject bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	disk := s.createDisk(bsd)
	if disk == 0 {
		return ErrUnavailable
	}
	defer s.f.cf.api.release(disk)
	token := nextNonzeroOperationToken()
	done := make(chan error, 1)
	operationStates.Store(token, done)
	operationFrameworks.Store(token, s.f)
	defer func() { operationStates.Delete(token); operationFrameworks.Delete(token) }()
	loop := s.f.cf.api.runLoopGetCurrent()
	s.f.da.api.scheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	defer s.f.da.api.unscheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	s.startOperation(disk, token, eject)
	return s.waitForOperation(ctx, done)
}

func (s *Session) createDisk(bsd string) uintptr {
	name := append([]byte("/dev/"+bsd), 0)
	return s.f.da.api.diskCreateBSD(0, s.ref, &name[0])
}

func nextNonzeroOperationToken() uintptr {
	token := uintptr(nextOperationToken.Add(1))
	if token == 0 {
		return uintptr(nextOperationToken.Add(1))
	}
	return token
}

func (s *Session) startOperation(disk, token uintptr, eject bool) {
	if eject {
		s.f.da.api.diskEject(disk, 0, operationCallback, token)
		return
	}
	s.f.da.api.diskUnmount(disk, 1, operationCallback, token)
}

func (s *Session) waitForOperation(ctx context.Context, done <-chan error) error {
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		default:
			s.pumpRunLoop()
		}
	}
}

func (s *Session) Unmount(ctx context.Context, bsd string) error { return s.operation(ctx, bsd, false) }
func (s *Session) Eject(ctx context.Context, bsd string) error   { return s.operation(ctx, bsd, true) }

var appearedCallback = purego.NewCallback(func(disk, context uintptr) { dispatchAppeared(context, disk) })

func waitForDisk(ctx context.Context, s *Session) (DiskDescription, error) {
	// Do not install a native callback for work that has already been
	// cancelled. Apart from making the result deterministic when a disk is
	// already present, this ensures that the callback context never outlives a
	// Session which returns immediately.
	if err := ctx.Err(); err != nil {
		return DiskDescription{}, err
	}
	state := newCallbackState(s)
	defer state.close()
	loop := s.f.cf.api.runLoopGetCurrent()
	s.f.da.api.registerAppeared(s.ref, 0, appearedCallback, state.token)
	s.f.da.api.scheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	defer func() {
		// DA retains callback registrations until they are explicitly removed.
		// Remove the registration before releasing its callback state or the
		// Session so a later run-loop delivery cannot use either one.
		s.f.da.api.unregisterCallback(s.ref, appearedCallback, state.token)
		s.f.da.api.unscheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	}()
	for {
		select {
		case r := <-state.result:
			return r.disk, r.err
		case <-ctx.Done():
			return DiskDescription{}, ctx.Err()
		default:
			s.f.cf.api.runLoopRunInMode(s.f.cf.defaultRunLoopMode, 0.05, true)
			runtime.Gosched()
			time.Sleep(time.Millisecond)
		}
	}
}
