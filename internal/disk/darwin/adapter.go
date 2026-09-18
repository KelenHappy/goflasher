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

// List returns one ProbeResult per whole disk, plus the number of whole disks
// whose registry identity could not be read. Those are never exposed, so the
// count is the only evidence the caller has that they existed.
func (a *NativeAdapter) List(ctx context.Context) ([]ProbeResult, int, error) {
	disks, e := a.listDisks(ctx)
	if e != nil {
		return nil, 0, e
	}
	out := make([]ProbeResult, 0, len(disks))
	skipped := 0
	for _, d := range disks {
		if !d.Whole {
			continue
		}
		r, e := a.probe(d, disks)
		if e != nil {
			skipped++ // incomplete identity is never exposed.
			continue
		}
		out = append(out, r)
	}
	return out, skipped, nil
}

// listDisks holds the arbitration session only for the enumeration itself. The
// registry identities read afterwards come from the frameworks, not the session.
func (a *NativeAdapter) listDisks(ctx context.Context) ([]native.DiskDescription, error) {
	s, e := a.frameworks.NewSession()
	if e != nil {
		return nil, e
	}
	defer s.Close()
	return s.ListDisks(ctx)
}

// probe pairs a whole disk with its registry identity and the mount points of
// its partitions.
func (a *NativeAdapter) probe(d native.DiskDescription, disks []native.DiskDescription) (ProbeResult, error) {
	i, e := a.frameworks.RegistryIdentity(d.BSDName)
	if e != nil {
		return ProbeResult{}, e
	}
	r := result(d, i)
	r.MountPoints = mountPoints(d.BSDName, disks)
	return r, nil
}

// mountPoints collects the volume paths of whole's partitions, which Disk
// Arbitration names whole + "s" + index.
func mountPoints(whole string, disks []native.DiskDescription) []string {
	var out []string
	for _, v := range disks {
		if strings.HasPrefix(v.BSDName, whole+"s") && v.VolumePath != "" {
			out = append(out, v.VolumePath)
		}
	}
	return out
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
	all, _, e := a.List(ctx)
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
