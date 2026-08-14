// Package privilege defines the ordinary-Go messages allowed across the
// privileged helper boundary. Native XPC handles remain in platform packages.
package privilege

import (
	"errors"
	"fmt"
)

const ProtocolVersion uint32 = 1

var (
	ErrIncompatible    = errors.New("privileged helper protocol is incompatible")
	ErrUnauthenticated = errors.New("privileged helper client is not authenticated")
	ErrInvalidTarget   = errors.New("privileged operation target is invalid")
)

// Target is selection evidence, not a raw locator. In particular it has no
// pathname field: the helper must re-enumerate and resolve the current node.
type Target struct {
	ID, RegistryID, MediaID, TransportSerial   string
	Capacity                                   uint64
	Whole, Removable, External, NonSystem, USB bool
}

type Request struct {
	Version       uint32
	Operation     string
	Target        Target
	Verify, Eject bool
}

func (r Request) Validate() error {
	if r.Version != ProtocolVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrIncompatible, r.Version, ProtocolVersion)
	}
	if r.Operation != "write" && r.Operation != "format" {
		return fmt.Errorf("unsupported privileged operation %q", r.Operation)
	}
	t := r.Target
	if t.ID == "" || t.RegistryID == "" || t.Capacity == 0 || !t.Whole || !t.Removable || !t.External || !t.NonSystem || !t.USB {
		return ErrInvalidTarget
	}
	return nil
}
