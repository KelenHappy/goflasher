package privilege

import (
	"errors"
	"reflect"
	"testing"
)

func validRequest() Request {
	return Request{Version: ProtocolVersion, Operation: "write", Target: Target{ID: "id", RegistryID: "registry", Capacity: 1, Whole: true, Removable: true, External: true, NonSystem: true, USB: true}}
}

func TestRequestValidationFailsClosed(t *testing.T) {
	r := validRequest()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.Target.RegistryID = ""
	if !errors.Is(r.Validate(), ErrInvalidTarget) {
		t.Fatalf("error=%v", r.Validate())
	}
	r = validRequest()
	r.Version++
	if !errors.Is(r.Validate(), ErrIncompatible) {
		t.Fatalf("error=%v", r.Validate())
	}
}

func TestTargetCannotCarryPathname(t *testing.T) {
	typ := reflect.TypeFor[Target]()
	for _, name := range []string{"Path", "Device", "DeviceNode", "BSDName", "ImagePath"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("Target unexpectedly exposes %s", name)
		}
	}
}

func TestInstallerSessionCannotCarryPathnameOrCommand(t *testing.T) {
	typ := reflect.TypeFor[SessionCommand]()
	for _, name := range []string{"Path", "Device", "DeviceNode", "FilesystemPath", "Command", "Executable"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Fatalf("SessionCommand unexpectedly exposes %s", name)
		}
	}
}

func TestInstallerSessionCommandsAreVersionedAndBounded(t *testing.T) {
	valid := SessionCommand{Version: ProtocolVersion, Kind: SessionWriteAt, Offset: 512, Length: 4096}
	if err := valid.Validate(8192); err != nil {
		t.Fatal(err)
	}
	for _, command := range []SessionCommand{
		{Version: ProtocolVersion - 1, Kind: SessionWriteAt, Length: 1},
		{Version: ProtocolVersion, Kind: SessionWriteAt, Offset: 8192, Length: 1},
		{Version: ProtocolVersion, Kind: "open-path", Length: 1},
	} {
		if err := command.Validate(8192); err == nil {
			t.Fatalf("command accepted: %+v", command)
		}
	}
}

func TestInstallerSessionRequestRequiresSectorGeometry(t *testing.T) {
	r := validRequest()
	r.Operation = OperationInstallerSession
	r.LogicalSectorSize = 512
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.LogicalSectorSize = 1000
	if !errors.Is(r.Validate(), ErrInvalidTarget) {
		t.Fatalf("error=%v", r.Validate())
	}
}
