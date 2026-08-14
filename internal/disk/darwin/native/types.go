//go:build darwin

// Package native is the only package allowed to expose the pointer-sized
// handles used by Apple's C frameworks. Callers receive ordinary Go values.
package native

import "errors"

var ErrUnavailable = errors.New("macOS native disk framework unavailable")

type DiskDescription struct {
	BSDName    string
	VolumePath string
	MediaName  string
	Size       uint64
	Whole      bool
	Internal   bool
	Ejectable  bool
	Removable  bool
}

type RegistryIdentity struct {
	EntryID, Path, Vendor, Product string
	MediaID, TransportSerial       string
	USBAncestor                    bool
}
