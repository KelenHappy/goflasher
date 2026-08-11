//go:build windows

// Package windows implements conservative removable USB disk discovery and
// raw access using Windows PowerShell storage cmdlets.
package windows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/progress"
)

var (
	ErrUnsupportedDevice = errors.New("unsupported device")
	ErrSystemDisk        = errors.New("system disk")
	ErrDeviceChanged     = errors.New("device identity changed")
	ErrUnmountFailed     = errors.New("could not take disk offline; run GoFlasher as administrator")
)

type commandRunner interface {
	Output(context.Context, string) ([]byte, error)
}

type powerShellRunner struct{}

func (powerShellRunner) Output(ctx context.Context, script string) ([]byte, error) {
	return exec.CommandContext(ctx, "powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script).CombinedOutput()
}

const (
	queryTimeout     = 30 * time.Second
	operationTimeout = 2 * time.Minute
	formatTimeout    = 10 * time.Minute
)

type Backend struct{ runner commandRunner }

var _ device.Backend = (*Backend)(nil)

func NewBackend() *Backend { return &Backend{runner: powerShellRunner{}} }

type diskJSON struct {
	Number         uint32   `json:"Number"`
	FriendlyName   string   `json:"FriendlyName"`
	SerialNumber   string   `json:"SerialNumber"`
	UniqueID       string   `json:"UniqueId"`
	Size           uint64   `json:"Size"`
	BusType        string   `json:"BusType"`
	IsBoot         bool     `json:"IsBoot"`
	IsSystem       bool     `json:"IsSystem"`
	IsOffline      bool     `json:"IsOffline"`
	PartitionCount int      `json:"PartitionCount"`
	MountPoints    []string `json:"MountPoints"`
	IsRemovable    bool     `json:"IsRemovable"`
}

const listScript = `$ErrorActionPreference='Stop'; $disks=@(
Get-Disk | ForEach-Object {
  $parts=@(Get-Partition -DiskNumber $_.Number -ErrorAction SilentlyContinue)
  $volumes=@($parts | Get-Volume -ErrorAction SilentlyContinue)
  $cim=Get-CimInstance Win32_DiskDrive -Filter ("Index=" + $_.Number) -ErrorAction SilentlyContinue
  [pscustomobject]@{
    Number=$_.Number; FriendlyName=$_.FriendlyName; SerialNumber=$_.SerialNumber
    UniqueId=$_.UniqueId; Size=$_.Size; BusType=[string]$_.BusType
    IsBoot=$_.IsBoot; IsSystem=$_.IsSystem; IsOffline=$_.IsOffline
    PartitionCount=$parts.Count
    MountPoints=@($volumes | Where-Object DriveLetter | ForEach-Object { ([string]$_.DriveLetter) + ':\' })
    IsRemovable=[bool]($cim.MediaType -match 'Removable')
  }
}); ConvertTo-Json -InputObject $disks -Compress`

func (b *Backend) list(ctx context.Context) ([]device.Device, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	out, err := b.runner.Output(ctx, listScript)
	if err != nil {
		return nil, fmt.Errorf("enumerate Windows disks: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	var raw []diskJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode Windows disks: %w", err)
	}
	result := make([]device.Device, 0, len(raw))
	for _, disk := range raw {
		serial := strings.TrimSpace(disk.SerialNumber)
		uniqueID := strings.TrimSpace(disk.UniqueID)
		path := `\\.\PhysicalDrive` + strconv.FormatUint(uint64(disk.Number), 10)
		id := uniqueID
		if id == "" {
			id = serial
		}
		d := device.Device{
			ID: id, Path: path, Model: strings.TrimSpace(disk.FriendlyName),
			Serial: serial, WWN: uniqueID, Transport: strings.ToLower(disk.BusType),
			SysfsPath: uniqueID, Major: disk.Number, Size: disk.Size,
			// Treat every online disk as mounted/unsafe for raw access. Set-Disk must
			// report it offline before OpenWriter/OpenReader can proceed.
			Mounted: !disk.IsOffline, MountPoints: disk.MountPoints, IsSystemDisk: disk.IsBoot || disk.IsSystem,
			PartitionCount: disk.PartitionCount,
		}
		switch {
		case d.IsSystemDisk:
			d.RejectReason = ErrSystemDisk.Error()
		case !strings.EqualFold(disk.BusType, "USB") || !disk.IsRemovable || disk.Size == 0 || id == "":
			d.RejectReason = ErrUnsupportedDevice.Error()
		default:
			d.IsAllowed = true
		}
		result = append(result, d)
	}
	return result, nil
}

func (b *Backend) ListAllowedDevices(ctx context.Context) ([]device.Device, error) {
	all, err := b.list(ctx)
	if err != nil {
		return nil, err
	}
	allowed := make([]device.Device, 0, len(all))
	for _, d := range all {
		if d.IsAllowed {
			allowed = append(allowed, d)
		}
	}
	return allowed, nil
}

func (b *Backend) RefreshDevice(ctx context.Context, id string) (device.Device, error) {
	all, err := b.list(ctx)
	if err != nil {
		return device.Device{}, err
	}
	for _, d := range all {
		if d.ID == id {
			return d, nil
		}
	}
	return device.Device{}, os.ErrNotExist
}

func (b *Backend) revalidate(ctx context.Context, selected device.Device) (device.Device, error) {
	fresh, err := b.RefreshDevice(ctx, selected.ID)
	if err != nil {
		return device.Device{}, fmt.Errorf("%w: %w", ErrDeviceChanged, err)
	}
	if !fresh.IsAllowed {
		return device.Device{}, fmt.Errorf("%w: %s", ErrUnsupportedDevice, fresh.RejectReason)
	}
	if !device.SameIdentity(selected, fresh) || selected.Size != fresh.Size || selected.Model != fresh.Model {
		return device.Device{}, ErrDeviceChanged
	}
	return fresh, nil
}

func (b *Backend) Unmount(ctx context.Context, selected device.Device) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	fresh, err := b.revalidate(ctx, selected)
	if err != nil {
		return err
	}
	script := fmt.Sprintf("$ErrorActionPreference='Stop'; Set-Disk -Number %d -IsOffline $true", fresh.Major)
	if out, err := b.runner.Output(ctx, script); err != nil {
		return fmt.Errorf("%w: %w: %s", ErrUnmountFailed, err, strings.TrimSpace(string(out)))
	}
	again, err := b.revalidate(ctx, fresh)
	if err != nil {
		return err
	}
	if again.Mounted {
		return ErrUnmountFailed
	}
	return nil
}

func (b *Backend) open(ctx context.Context, selected device.Device, flag int) (*os.File, error) {
	fresh, err := b.revalidate(ctx, selected)
	if err != nil {
		return nil, err
	}
	if !fresh.Mounted {
		return os.OpenFile(fresh.Path, flag, 0)
	}
	return nil, ErrUnmountFailed
}

func (b *Backend) OpenWriter(ctx context.Context, d device.Device) (io.WriteCloser, error) {
	return b.open(ctx, d, os.O_WRONLY)
}
func (b *Backend) OpenReader(ctx context.Context, d device.Device) (io.ReadCloser, error) {
	return b.open(ctx, d, os.O_RDONLY)
}
func (b *Backend) Flush(ctx context.Context, d device.Device) error {
	f, err := b.open(ctx, d, os.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (b *Backend) Eject(ctx context.Context, d device.Device) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	fresh, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	if fresh.Mounted {
		return ErrUnmountFailed
	}
	return nil // An offline disk is in Windows' safe-removal state.
}

func (b *Backend) FormatFAT32(ctx context.Context, d device.Device, label string, updates chan<- progress.Update) error {
	ctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()
	fresh, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	// Literal labels are escaped before interpolation. Clear-Disk and the
	// explicit MBR initialization make this a full-device, Rufus-style format.
	label = strings.ReplaceAll(label, "'", "''")
	script := fmt.Sprintf("$ErrorActionPreference='Stop'; Set-Disk -Number %d -IsOffline $false; Clear-Disk -Number %d -RemoveData -RemoveOEM -Confirm:$false; Initialize-Disk -Number %d -PartitionStyle MBR; $p=New-Partition -DiskNumber %d -UseMaximumSize -AssignDriveLetter; Format-Volume -Partition $p -FileSystem FAT32 -NewFileSystemLabel '%s' -Confirm:$false -Force | Out-Null", fresh.Major, fresh.Major, fresh.Major, fresh.Major, label)
	if out, err := b.runner.Output(ctx, script); err != nil {
		return fmt.Errorf("format FAT32: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
