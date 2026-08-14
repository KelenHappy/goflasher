//go:build darwin

// Package darwin contains platform policy and adapters expressed only in Go
// values. Apple framework handles remain in the nested native package.
package darwin

import (
	"context"
	"strings"

	"github.com/goflasher/goflasher/internal/disk/darwin/native"
)

type ProbeResult struct {
	BSDName, MediaName                        string
	Size                                      uint64
	Whole, Internal, Ejectable, Removable     bool
	RegistryID, RegistryPath                  string
	Vendor, Product, MediaID, TransportSerial string
	USBAncestor                               bool
	MountPoints                               []string
}

type Adapter interface {
	List(context.Context) ([]ProbeResult, error)
	Describe(context.Context, string) (ProbeResult, error)
	WaitForDisk(context.Context) (ProbeResult, error)
	Unmount(context.Context, string) error
	Eject(context.Context, string) error
}

func (a *NativeAdapter) List(ctx context.Context) ([]ProbeResult, error) {
	s, e := a.frameworks.NewSession()
	if e != nil {
		return nil, e
	}
	defer s.Close()
	disks, e := s.ListDisks(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]ProbeResult, 0, len(disks))
	for _, d := range disks {
		if !d.Whole {
			continue
		}
		i, err := a.frameworks.RegistryIdentity(d.BSDName)
		if err != nil {
			continue
		} // incomplete identity is never exposed.
		r := result(d, i)
		for _, volume := range disks {
			if strings.HasPrefix(volume.BSDName, d.BSDName+"s") && volume.VolumePath != "" {
				r.MountPoints = append(r.MountPoints, volume.VolumePath)
			}
		}
		out = append(out, r)
	}
	return out, nil
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
	return ProbeResult{BSDName: d.BSDName, MediaName: d.MediaName, Size: d.Size, Whole: d.Whole, Internal: d.Internal, Ejectable: d.Ejectable, Removable: d.Removable, RegistryID: i.EntryID, RegistryPath: i.Path, Vendor: i.Vendor, Product: i.Product, MediaID: i.MediaID, TransportSerial: i.TransportSerial, USBAncestor: i.USBAncestor}
}
func (a *NativeAdapter) Describe(ctx context.Context, bsd string) (ProbeResult, error) {
	all, e := a.List(ctx)
	if e != nil {
		return ProbeResult{}, e
	}
	for _, d := range all {
		if d.BSDName == bsd {
			return d, nil
		}
	}
	return ProbeResult{}, native.ErrUnavailable
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
func (a *NativeAdapter) Unmount(ctx context.Context, bsd string) error {
	s, e := a.frameworks.NewSession()
	if e != nil {
		return e
	}
	defer s.Close()
	return s.Unmount(ctx, bsd)
}
func (a *NativeAdapter) Eject(ctx context.Context, bsd string) error {
	s, e := a.frameworks.NewSession()
	if e != nil {
		return e
	}
	defer s.Close()
	return s.Eject(ctx, bsd)
}
