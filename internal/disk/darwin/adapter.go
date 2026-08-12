//go:build darwin

// Package darwin contains platform policy and adapters expressed only in Go
// values. Apple framework handles remain in the nested native package.
package darwin

import (
	"context"

	"github.com/goflasher/goflasher/internal/disk/darwin/native"
)

type ProbeResult struct {
	BSDName, MediaName                    string
	Size                                  uint64
	Whole, Internal, Ejectable, Removable bool
	RegistryID, RegistryPath              string
	Vendor, Product, Serial               string
	USBAncestor                           bool
}

type Adapter interface {
	Describe(context.Context, string) (ProbeResult, error)
	WaitForDisk(context.Context) (ProbeResult, error)
}

type NativeAdapter struct{ frameworks *native.Frameworks }

func OpenNativeAdapter() (*NativeAdapter, error) {
	f, e := native.OpenFrameworks()
	if e != nil {
		return nil, e
	}
	return &NativeAdapter{frameworks: f}, nil
}
func result(d native.DiskDescription, i native.RegistryIdentity) ProbeResult {
	return ProbeResult{BSDName: d.BSDName, MediaName: d.MediaName, Size: d.Size, Whole: d.Whole, Internal: d.Internal, Ejectable: d.Ejectable, Removable: d.Removable, RegistryID: i.EntryID, RegistryPath: i.Path, Vendor: i.Vendor, Product: i.Product, Serial: i.Serial, USBAncestor: i.USBAncestor}
}
func (a *NativeAdapter) Describe(_ context.Context, bsd string) (ProbeResult, error) {
	s, e := a.frameworks.NewSession()
	if e != nil {
		return ProbeResult{}, e
	}
	defer s.Close()
	d, e := s.DiskFromBSDName(bsd)
	if e != nil {
		return ProbeResult{}, e
	}
	i, e := a.frameworks.RegistryIdentity(bsd)
	return result(d, i), e
}
func (a *NativeAdapter) WaitForDisk(ctx context.Context) (ProbeResult, error) {
	s, e := a.frameworks.NewSession()
	if e != nil {
		return ProbeResult{}, e
	}
	defer s.Close()
	d, e := s.WaitForDisk(ctx)
	if e != nil {
		return ProbeResult{}, e
	}
	i, e := a.frameworks.RegistryIdentity(d.BSDName)
	return result(d, i), e
}
