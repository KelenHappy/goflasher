//go:build linux

// usbwriter-hwtest is an explicit, destructive real-device smoke test. It is
// never used by CI and refuses to write unless the exact device path is echoed
// in the confirmation flag.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/goflasher/goflasher/internal/app"
	"github.com/goflasher/goflasher/internal/device"
	"github.com/goflasher/goflasher/internal/image"
	linuxbackend "github.com/goflasher/goflasher/internal/linux"
	"github.com/goflasher/goflasher/internal/progress"
)

func main() {
	devicePath := flag.String("device", "", "allowed USB device path")
	imagePath := flag.String("image", "", "small disposable test image")
	confirm := flag.String("confirm-device", "", "must exactly match --device to enable destructive writing")
	flag.Parse()
	ctx := context.Background()
	backend := linuxbackend.NewBackend()
	devices, err := backend.ListAllowedDevices(ctx)
	fatal(err)
	if *devicePath == "" {
		for _, d := range devices {
			fmt.Printf("%s %s %s %d bytes serial=%s card-reader=%v mounted=%v\n", d.Path, d.Vendor, d.Model, d.Size, d.Serial, d.IsCardReader, d.Mounted)
		}
		return
	}
	if *imagePath == "" || *confirm != *devicePath {
		fatal(fmt.Errorf("refusing destructive test: --image and --confirm-device=%s are required", *devicePath))
	}
	var selectedFound bool
	var selected device.Device
	for _, d := range devices {
		if d.Path == *devicePath {
			selected = d
			selectedFound = true
			break
		}
	}
	if !selectedFound {
		fatal(fmt.Errorf("device is not in conservative allow-list: %s", *devicePath))
	}
	info, err := image.Detect(*imagePath)
	fatal(err)
	info, err = image.Inspect(info)
	fatal(err)
	fmt.Printf("DESTROYING %s (%s %s, serial %s) with %s (%d bytes)\n", selected.Path, selected.Vendor, selected.Model, selected.Serial, info.Path, info.UncompressedSize)
	states := app.NewStateMachine()
	for _, state := range []app.State{app.ImageSelected, app.Ready, app.Confirming} {
		fatal(states.Transition(state))
	}
	updates := make(chan progress.Update, 16)
	go func() {
		for u := range updates {
			fmt.Printf("\r%-10s %d/%d %.1f MiB/s", u.Stage, u.BytesProcessed, u.TotalBytes, u.BytesPerSecond/(1<<20))
		}
	}()
	result, err := (&app.Service{Backend: backend, State: states}).Run(ctx, info, selected, app.RunOptions{Verify: true}, updates)
	close(updates)
	fmt.Println()
	fatal(err)
	fmt.Printf("verified=%v sha256=%s bytes=%d\n", result.Verified, result.TargetSHA256, result.BytesWritten)
}
func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
