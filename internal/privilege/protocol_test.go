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
