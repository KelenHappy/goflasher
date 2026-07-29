# GoFlasher

GoFlasher is a Linux-only, safety-first USB image writer. The repository now
includes the Phase 3 write/flush/read-back/eject pipeline and the
Phase 4 single-page Fyne UI. The final privileged-helper architecture has not
yet been implemented.

The core already provides UI-independent contracts, raw/gzip/xz streaming
image readers, SHA-256 calculation, cancellable file-to-file writing, and
progress/speed/ETA reporting. It never expands a compressed image to a
temporary file.

The Linux backend enumerates `/sys/class/block`, supplements kernel topology
with udev properties, reads mount and swap ownership from procfs, and only
returns positively identified removable USB flash media or card readers. It
rejects system disks, ATA/SSD/bridge devices, and ambiguous USB storage. Device
identity is revalidated before unmounting or opening a target. Unmount and
power-off currently use `udisksctl`; this is an explicit Phase 2 boundary, not
a request to launch the GUI as root.

## GUI

Fyne dependencies require the usual Linux OpenGL/X11 development packages.
Build or run the single-page UI with:

```sh
go run -tags fyne ./cmd/usbwriter
```

Without the `fyne` tag, the command builds a dependency-free informational
launcher so headless core CI does not require graphical system libraries.

## Explicit real-device smoke test

Listing candidates is non-destructive:

```sh
go run ./cmd/usbwriter-hwtest
```

Writing is intentionally difficult to invoke and **destroys all target data**:

```sh
go run ./cmd/usbwriter-hwtest \
  --device /dev/sdX --image ./small-test.img --confirm-device /dev/sdX
```

The tool refuses devices outside the same conservative backend allow-list and
always performs read-back verification. Never run this command in ordinary CI.

## Development

Requires Go 1.25 or newer.

```sh
go test ./...
```

Automated tests deliberately never access real block devices.
