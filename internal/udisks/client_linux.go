//go:build linux

// Package udisks provides the small part of the UDisks2 D-Bus API used by
// GoFlasher. It intentionally resolves objects by the Block.Device property
// instead of constructing UDisks object paths from kernel device names.
package udisks

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	busName           = "org.freedesktop.UDisks2"
	managerPath       = dbus.ObjectPath("/org/freedesktop/UDisks2")
	objectManager     = "org.freedesktop.DBus.ObjectManager.GetManagedObjects"
	blockInterface    = "org.freedesktop.UDisks2.Block"
	filesystemUnmount = "org.freedesktop.UDisks2.Filesystem.Unmount"
	drivePowerOff     = "org.freedesktop.UDisks2.Drive.PowerOff"
)

// Client performs mount-management operations through the system D-Bus.
type Client interface {
	Unmount(context.Context, string) error
	PowerOff(context.Context, string) error
}

type client struct {
	connect func(...dbus.ConnOption) (*dbus.Conn, error)
}

// New returns a client that connects directly to the UDisks2 system service.
func New() Client { return &client{connect: dbus.ConnectSystemBus} }

func (c *client) Unmount(ctx context.Context, device string) error {
	conn, objects, err := c.objects(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	path, _, err := blockForDevice(objects, device)
	if err != nil {
		return err
	}
	return conn.Object(busName, path).CallWithContext(ctx, filesystemUnmount, 0, map[string]dbus.Variant{}).Err
}

func (c *client) PowerOff(ctx context.Context, device string) error {
	conn, objects, err := c.objects(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, drive, err := blockForDevice(objects, device)
	if err != nil {
		return err
	}
	if !drive.IsValid() || drive == dbus.ObjectPath("/") {
		return fmt.Errorf("UDisks2 block device %q has no drive", device)
	}
	return conn.Object(busName, drive).CallWithContext(ctx, drivePowerOff, 0, map[string]dbus.Variant{}).Err
}

type managedObjects map[dbus.ObjectPath]map[string]map[string]dbus.Variant

func (c *client) objects(ctx context.Context) (*dbus.Conn, managedObjects, error) {
	conn, err := c.connect()
	if err != nil {
		return nil, nil, fmt.Errorf("connect to system D-Bus: %w", err)
	}
	var objects managedObjects
	call := conn.Object(busName, managerPath).CallWithContext(ctx, objectManager, 0)
	if err := call.Store(&objects); err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("query UDisks2 objects: %w", err)
	}
	return conn, objects, nil
}

func blockForDevice(objects managedObjects, device string) (dbus.ObjectPath, dbus.ObjectPath, error) {
	for path, interfaces := range objects {
		properties, ok := interfaces[blockInterface]
		if !ok {
			continue
		}
		value, ok := properties["Device"]
		if !ok {
			continue
		}
		bytes, ok := value.Value().([]byte)
		if !ok || string(bytes) != device+"\x00" {
			continue
		}
		drive, _ := properties["Drive"].Value().(dbus.ObjectPath)
		return path, drive, nil
	}
	return "", "", fmt.Errorf("UDisks2 block device %q not found", device)
}
