package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/goflasher/goflasher/internal/blockio"
)

var errMisalignedSector = errors.New("request is not whole 512-byte sectors")

// sectorStrictFile rejects any request that is not whole 512-byte sectors, as
// a Windows \\.\PhysicalDriveN handle does.
type sectorStrictFile struct{ *os.File }

func checkSectors(p []byte, off int64) error {
	if off%512 != 0 || len(p)%512 != 0 {
		return fmt.Errorf("%w: off=%d len=%d", errMisalignedSector, off, len(p))
	}
	return nil
}

func (f sectorStrictFile) ReadAt(p []byte, off int64) (int, error) {
	if err := checkSectors(p, off); err != nil {
		return 0, err
	}
	return f.File.ReadAt(p, off)
}

func (f sectorStrictFile) WriteAt(p []byte, off int64) (int, error) {
	if err := checkSectors(p, off); err != nil {
		return 0, err
	}
	return f.File.WriteAt(p, off)
}

type alignedSession struct {
	*blockio.Aligned
	file *os.File
}

func (s alignedSession) Sync() error  { return s.file.Sync() }
func (s alignedSession) Close() error { return s.file.Close() }

func TestWindowsInstallerOnSectorStrictTargetRequiresAlignment(t *testing.T) {
	fixture := newWindowsExecutorFixture(t)
	fixture.backend.session = func(f *os.File) (installerSession, error) { return sectorStrictFile{f}, nil }
	_, err := fixture.service().Run(context.Background(), RunRequest{Image: fixture.info, Target: fixture.target})
	assertErrorIs(t, err, errMisalignedSector)
}

func TestWindowsInstallerOnSectorStrictTargetThroughAlignedAdapter(t *testing.T) {
	fixture := newWindowsExecutorFixture(t)
	fixture.backend.session = func(f *os.File) (installerSession, error) {
		aligned, err := blockio.NewAligned(sectorStrictFile{f}, 512, fixture.target.Size)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		return alignedSession{Aligned: aligned, file: f}, nil
	}
	result, err := fixture.service().Run(context.Background(), RunRequest{Image: fixture.info, Target: fixture.target, Options: RunOptions{Eject: true}})
	assertNoError(t, err)
	assertEqual(t, "plan kind", result.PlanKind, PlanWindowsInstaller)
	assertEqual(t, "semantic verification", result.SemanticVerified, true)
	assertPositive(t, "files written", result.FilesWritten)
	assertEqual(t, "state", fixture.state.State(), Completed)
}
