//go:build darwin

// Package macos implements conservative removable USB disk discovery and raw
// access using macOS diskutil.
package macos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/goflasher/goflasher/internal/progress"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/goflasher/goflasher/internal/device"
)

var (
	ErrUnsupportedDevice = errors.New("unsupported device")
	ErrSystemDisk        = errors.New("system disk")
	ErrDeviceChanged     = errors.New("device identity changed")
	ErrUnmountFailed     = errors.New("unmount failed; raw access may require administrator privileges")
)

type commandRunner interface {
	JSON(context.Context, ...string) ([]byte, error)
	Run(context.Context, ...string) ([]byte, error)
}

type diskutilRunner struct{}

func (diskutilRunner) JSON(ctx context.Context, args ...string) ([]byte, error) {
	commandArgs := append([]string{args[0], "-plist"}, args[1:]...)
	plist, err := exec.CommandContext(ctx, "diskutil", commandArgs...).Output()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "--", "-")
	cmd.Stdin = bytes.NewReader(plist)
	return cmd.Output()
}

func (diskutilRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "diskutil", args...).CombinedOutput()
}

type Backend struct{ runner commandRunner }

var _ device.Backend = (*Backend)(nil)

func NewBackend() *Backend { return &Backend{runner: diskutilRunner{}} }

type listJSON struct {
	Disks []listedDisk `json:"AllDisksAndPartitions"`
}
type listedDisk struct {
	DeviceIdentifier string            `json:"DeviceIdentifier"`
	Partitions       []listedPartition `json:"Partitions"`
}
type listedPartition struct {
	MountPoint string `json:"MountPoint"`
}
type infoJSON struct {
	DeviceIdentifier    string `json:"DeviceIdentifier"`
	DeviceNode          string `json:"DeviceNode"`
	DeviceTreePath      string `json:"DeviceTreePath"`
	MediaName           string `json:"MediaName"`
	IORegistryEntryName string `json:"IORegistryEntryName"`
	BusProtocol         string `json:"BusProtocol"`
	TotalSize           uint64 `json:"TotalSize"`
	Whole               bool   `json:"Whole"`
	Internal            bool   `json:"Internal"`
	RemovableMedia      bool   `json:"RemovableMedia"`
	Ejectable           bool   `json:"Ejectable"`
}

func (b *Backend) list(ctx context.Context) ([]device.Device, error) {
	out, err := b.runner.JSON(ctx, "list", "external", "physical")
	if err != nil {
		return nil, fmt.Errorf("list macOS disks: %w", err)
	}
	var listing listJSON
	if err := json.Unmarshal(out, &listing); err != nil {
		return nil, fmt.Errorf("decode macOS disk list: %w", err)
	}
	result := make([]device.Device, 0, len(listing.Disks))
	for _, listed := range listing.Disks {
		infoOut, err := b.runner.JSON(ctx, "info", listed.DeviceIdentifier)
		if err != nil {
			continue
		}
		var info infoJSON
		if json.Unmarshal(infoOut, &info) != nil {
			continue
		}
		// DeviceTreePath identifies the physical attachment and is independent of
		// partition contents, unlike filesystem/disk UUIDs overwritten by a flash.
		id := strings.TrimSpace(info.DeviceTreePath)
		number, _ := strconv.ParseUint(strings.TrimPrefix(info.DeviceIdentifier, "disk"), 10, 32)
		mounts := make([]string, 0, len(listed.Partitions))
		for _, partition := range listed.Partitions {
			if partition.MountPoint != "" {
				mounts = append(mounts, partition.MountPoint)
			}
		}
		d := device.Device{
			ID: id, Path: rawDevice(info.DeviceNode), Model: first(info.MediaName, info.IORegistryEntryName),
			Transport: strings.ToLower(info.BusProtocol), SysfsPath: info.DeviceTreePath,
			Major: uint32(number), Size: info.TotalSize, IsCardReader: info.Ejectable,
			Mounted: len(mounts) > 0, MountPoints: mounts, PartitionCount: len(listed.Partitions),
			IsSystemDisk: info.Internal,
		}
		switch {
		case d.IsSystemDisk:
			d.RejectReason = ErrSystemDisk.Error()
		case !info.Whole || !strings.EqualFold(info.BusProtocol, "USB") || !info.RemovableMedia || info.TotalSize == 0 || id == "":
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
	fresh, err := b.revalidate(ctx, selected)
	if err != nil {
		return err
	}
	if out, err := b.runner.Run(ctx, "unmountDisk", wholeDevice(fresh.Path)); err != nil {
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
	if fresh.Mounted {
		return nil, ErrUnmountFailed
	}
	return os.OpenFile(fresh.Path, flag, 0)
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
	fresh, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	out, err := b.runner.Run(ctx, "eject", wholeDevice(fresh.Path))
	if err != nil {
		return fmt.Errorf("eject: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (b *Backend) FormatFAT32(ctx context.Context, d device.Device, label string, updates chan<- progress.Update) error {
	fresh, err := b.revalidate(ctx, d)
	if err != nil {
		return err
	}
	if label == "" {
		label = "GOFLASHER"
	}
	out, err := b.runner.Run(ctx, "eraseDisk", "FAT32", label, "MBRFormat", wholeDevice(fresh.Path))
	if err != nil {
		return fmt.Errorf("format FAT32: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func rawDevice(path string) string {
	if strings.HasPrefix(path, "/dev/disk") {
		return strings.Replace(path, "/dev/disk", "/dev/rdisk", 1)
	}
	return path
}
func wholeDevice(path string) string { return strings.Replace(path, "/dev/rdisk", "/dev/disk", 1) }
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
