//go:build darwin

package native

import (
	"context"
	"errors"
	"fmt"
	"runtime"
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
	scheduleRunLoop     func(uintptr, uintptr, uintptr)
	unscheduleRunLoop   func(uintptr, uintptr, uintptr)
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
	purego.RegisterLibFunc(&d.api.scheduleRunLoop, lib, "DASessionScheduleWithRunLoop")
	purego.RegisterLibFunc(&d.api.unscheduleRunLoop, lib, "DASessionUnscheduleFromRunLoop")
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
	// Look up every currently bound public key even where conversion (for
	// example CFURL for VolumePath) belongs to a later phase.
	get("kDADiskDescriptionVolumePathKey")
	s.emitDiagnostic(diagnostic)
	if out.BSDName == "" {
		return DiskDescription{}, errors.New("DADiskGetBSDName and media BSD-name description were both empty")
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

var appearedCallback = purego.NewCallback(func(disk, context uintptr) { dispatchAppeared(context, disk) })

func waitForDisk(ctx context.Context, s *Session) (DiskDescription, error) {
	state := newCallbackState(s)
	defer state.close()
	loop := s.f.cf.api.runLoopGetCurrent()
	s.f.da.api.registerAppeared(s.ref, 0, appearedCallback, state.token)
	s.f.da.api.scheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
	defer s.f.da.api.unscheduleRunLoop(s.ref, loop, s.f.cf.defaultRunLoopMode)
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
