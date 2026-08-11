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
	ErrUnmountFailed     = errors.New("unmount failed; raw access may require administrator privileges")
)

type commandRunner interface {
	JSON(context.Context, ...string) ([]byte, error)
	Run(context.Context, ...string) ([]byte, error)
}

type diskutilRunner struct{}

func (diskutilRunner) JSON(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("diskutil: missing command")
	}
	commandArgs := append([]string{args[0], "-plist"}, args[1:]...)
	plist, err := exec.CommandContext(ctx, "diskutil", commandArgs...).CombinedOutput()
	if err != nil {
		return nil, commandError("diskutil", err, plist)
	}
	cmd := exec.CommandContext(ctx, "plutil", "-convert", "json", "-o", "-", "--", "-")
	cmd.Stdin = bytes.NewReader(plist)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, commandError("plutil", err, out)
	}
	return out, nil
}

func (diskutilRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, "diskutil", args...).CombinedOutput()
}

func commandError(command string, err error, output []byte) error {
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("%s: %w", command, err)
	}
	return fmt.Errorf("%s: %w: %s", command, err, detail)
}

const (
	queryTimeout     = 30 * time.Second
	operationTimeout = 2 * time.Minute
	formatTimeout    = 10 * time.Minute
)

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
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
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
		if _, ok := wholeDiskNumber(listed.DeviceIdentifier); !ok {
			continue
		}
		infoOut, err := b.runner.JSON(ctx, "info", listed.DeviceIdentifier)
		if err != nil {
			return nil, fmt.Errorf("inspect macOS disk %q: %w", listed.DeviceIdentifier, err)
		}
		var info infoJSON
		if err := json.Unmarshal(infoOut, &info); err != nil {
			return nil, fmt.Errorf("decode macOS disk %q info: %w", listed.DeviceIdentifier, err)
		}
		if d, ok := deviceFromInfo(listed, info); ok {
			result = append(result, d)
		}
	}
	return result, nil
}

func deviceFromInfo(listed listedDisk, info infoJSON) (device.Device, bool) {
	number, ok := wholeDiskNumber(listed.DeviceIdentifier)
	if !ok || info.DeviceIdentifier != listed.DeviceIdentifier || info.DeviceNode != "/dev/"+listed.DeviceIdentifier {
		return device.Device{}, false
	}
	mounts := partitionMounts(listed.Partitions)
	id := strings.TrimSpace(info.DeviceTreePath)
	d := device.Device{
		ID: id, Path: "/dev/r" + listed.DeviceIdentifier, Model: first(info.MediaName, info.IORegistryEntryName),
		Transport: strings.ToLower(info.BusProtocol), SysfsPath: id,
		Major: number, Size: info.TotalSize, IsCardReader: info.Ejectable,
		Mounted: len(mounts) > 0, MountPoints: mounts, PartitionCount: len(listed.Partitions), IsSystemDisk: info.Internal,
	}
	classifyDevice(&d, info)
	return d, true
}

func wholeDiskNumber(identifier string) (uint32, bool) {
	digits := strings.TrimPrefix(identifier, "disk")
	if digits == identifier || digits == "" {
		return 0, false
	}
	number, err := strconv.ParseUint(digits, 10, 32)
	return uint32(number), err == nil
}

func partitionMounts(partitions []listedPartition) []string {
	mounts := make([]string, 0, len(partitions))
	for _, partition := range partitions {
		if partition.MountPoint != "" {
			mounts = append(mounts, partition.MountPoint)
		}
	}
	return mounts
}

func classifyDevice(d *device.Device, info infoJSON) {
	switch {
	case d.IsSystemDisk:
		d.RejectReason = ErrSystemDisk.Error()
	case !info.Whole || !strings.EqualFold(info.BusProtocol, "USB") || !info.RemovableMedia || info.TotalSize == 0 || d.ID == "":
		d.RejectReason = ErrUnsupportedDevice.Error()
	default:
		d.IsAllowed = true
	}
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
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
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
	ctx, cancel := context.WithTimeout(ctx, formatTimeout)
	defer cancel()
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

func wholeDevice(path string) string { return strings.Replace(path, "/dev/rdisk", "/dev/disk", 1) }
func first(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
