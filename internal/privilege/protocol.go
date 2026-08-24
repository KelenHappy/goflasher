// Package privilege defines the ordinary-Go messages allowed across the
// privileged helper boundary. Native XPC handles remain in platform packages.
package privilege

import (
	"errors"
	"fmt"
)

const ProtocolVersion uint32 = 2

const (
	OperationWrite            = "write"
	OperationFormat           = "format"
	OperationInstallerSession = "installer-session"
	SessionWriteAt            = "write-at"
	SessionReadAt             = "read-at"
	SessionFlush              = "flush"
	SessionCancel             = "cancel"
	SessionClose              = "close"
)

var (
	ErrIncompatible    = errors.New("privileged helper protocol is incompatible")
	ErrUnauthenticated = errors.New("privileged helper client is not authenticated")
	ErrInvalidTarget   = errors.New("privileged operation target is invalid")
)

// Target is selection evidence, not a raw locator. In particular it has no
// pathname field: the helper must re-enumerate and resolve the current node.
// The boolean claims below (Whole, Removable, External, NonSystem, USB) are
// asserted by the untrusted client, not verified facts. The helper must
// independently confirm each property against the resolved device before
// authorizing any destructive operation; it must never trust them as-is.
type Target struct {
	ID, RegistryID, MediaID, TransportSerial   string
	Capacity                                   uint64
	Whole, Removable, External, NonSystem, USB bool
}

type Request struct {
	Version           uint32
	Operation         string
	Target            Target
	Verify, Eject     bool
	LogicalSectorSize uint32
}

type SessionCommand struct {
	Version uint32 `json:"version"`
	Kind    string `json:"kind"`
	Offset  uint64 `json:"offset,omitempty"`
	Length  uint32 `json:"length,omitempty"`
}

type SessionResponse struct {
	Version uint32 `json:"version"`
	OK      bool   `json:"ok"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	Length  uint32 `json:"length,omitempty"`
}

type SessionError struct{ Code, Message string }

func (e *SessionError) Error() string {
	return fmt.Sprintf("privileged session %s: %s", e.Code, e.Message)
}

// Validate is a well-formedness check on the request, NOT a security boundary.
// It confirms the protocol version, a supported operation, and that the client
// has supplied the required fields — including asserting the Target's safety
// booleans are all true. It does NOT establish that the device actually has
// those properties: the booleans are client claims. Authorization must re-derive
// and re-verify the device on the privileged helper side (as the Linux helper
// does via kernel major/minor and fstat) before any destructive operation.
func (r Request) Validate() error {
	if r.Version != ProtocolVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrIncompatible, r.Version, ProtocolVersion)
	}
	if r.Operation != OperationWrite && r.Operation != OperationFormat && r.Operation != OperationInstallerSession {
		return fmt.Errorf("unsupported privileged operation %q", r.Operation)
	}
	if r.Operation == OperationInstallerSession && (r.LogicalSectorSize < 512 || r.LogicalSectorSize&(r.LogicalSectorSize-1) != 0) {
		return ErrInvalidTarget
	}
	t := r.Target
	if !t.hasRequiredClaims() {
		return ErrInvalidTarget
	}
	return nil
}

func (t Target) hasRequiredClaims() bool {
	if t.ID == "" {
		return false
	}
	if t.RegistryID == "" {
		return false
	}
	if t.Capacity == 0 {
		return false
	}
	if !t.Whole {
		return false
	}
	if !t.Removable {
		return false
	}
	if !t.External {
		return false
	}
	if !t.NonSystem {
		return false
	}
	return t.USB
}

func (c SessionCommand) Validate(capacity uint64) error {
	if c.Version != ProtocolVersion {
		return ErrIncompatible
	}
	switch c.Kind {
	case SessionWriteAt, SessionReadAt:
		if c.Length == 0 || c.Offset > capacity || uint64(c.Length) > capacity-c.Offset {
			return ErrInvalidTarget
		}
	case SessionFlush, SessionCancel, SessionClose:
		if c.Offset != 0 || c.Length != 0 {
			return ErrInvalidTarget
		}
	default:
		return ErrInvalidTarget
	}
	return nil
}
