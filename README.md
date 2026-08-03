<div align="center">
  <img src="packaging/org.goflasher.usbwriter.svg" width="128" height="128" alt="GoFlasher logo">

# GoFlasher: A Safety-First USB Image Writer

[![CI](https://github.com/KelenHappy/goflasher/actions/workflows/ci.yml/badge.svg)](https://github.com/KelenHappy/goflasher/actions/workflows/ci.yml)
[![Release packages](https://github.com/KelenHappy/goflasher/actions/workflows/release.yml/badge.svg)](https://github.com/KelenHappy/goflasher/actions/workflows/release.yml)
[![Latest release](https://img.shields.io/github/v/release/KelenHappy/goflasher?display_name=tag)](https://github.com/KelenHappy/goflasher/releases/latest)
[![License: GPL v3](https://img.shields.io/github/license/KelenHappy/goflasher)](LICENSE)
[![Downloads](https://img.shields.io/github/downloads/KelenHappy/goflasher/total)](https://github.com/KelenHappy/goflasher/releases)
[![Contributors](https://img.shields.io/github/contributors/KelenHappy/goflasher)](https://github.com/KelenHappy/goflasher/graphs/contributors)

**GoFlasher writes raw or compressed disk images to removable USB flash media on Linux.**
</div>

> [!WARNING]
> Writing an image destroys all data on the selected device. Check its model,
> serial number, capacity, and device path before confirming.

## Features

- Write `.iso`, `.img`, and `.raw` disk images.
- Stream gzip (`.gz`) and XZ (`.xz`) images without creating an uncompressed
  temporary file.
- Calculate the source SHA-256 while inspecting the image and while writing it.
- Optionally read the written bytes back and compare their SHA-256 checksum.
- Show writing and verification progress, throughput, and estimated time.
- Cancel an active operation.
- Optionally power off the USB device after a successful write.
- Open the Linux desktop's native file chooser through XDG Desktop Portal.
- Display the interface in English or Traditional Chinese.
- Keep a copyable, bounded activity log in the application.
- Restrict targets to devices positively identified as removable USB flash
  media or card readers.
- Reject mounted critical system disks, swap devices, ATA devices, SSD/HDD
  models, storage bridges, UAS devices, and ambiguous USB storage.
- Revalidate device identity before unmounting and before raw-device access.

GoFlasher is an image writer. It does **not** format filesystems, download
operating-system images, create Windows installation workarounds, create
persistent partitions, or perform bad-block tests.

## Platform status

The production GUI and raw-device backend currently support **Linux only**.
The platform-neutral packages are tested on Linux and Windows, and the native
file-picker boundary is separated by build tags so a Windows backend can be
implemented later. There is no Windows writer or Windows GUI release yet.

The current backend reads Linux sysfs, procfs, and udev information. It uses
`udisksctl` for unmount and power-off operations. The GUI must not be run as
root. A dedicated privileged helper has not been implemented.

## Downloads

When a packaged release is published, its GitHub release assets are produced by
the repository release workflow:

- `GoFlasher-<version>-x86_64.AppImage`
- `goflasher_<version>_amd64.deb`
- `SHA256SUMS`

Verify files downloaded from the release page before running or installing
them:

```sh
sha256sum --check SHA256SUMS
```

Run the AppImage:

```sh
chmod +x GoFlasher-*-x86_64.AppImage
./GoFlasher-*-x86_64.AppImage
```

Install the Debian package on Debian or Ubuntu:

```sh
sudo apt install ./goflasher_*_amd64.deb
```

Do not launch GoFlasher itself with `sudo`.

## Building

GoFlasher requires the Go version declared in [`go.mod`](go.mod). Building the
Fyne GUI also requires Linux OpenGL, X11, and Wayland development packages.
See **[BUILDING.md](BUILDING.md)** for dependency installation, source builds,
AppImage and Debian packaging, and current Windows limitations.

A development GUI build can be started with:

```sh
go run -tags fyne ./cmd/usbwriter
```

Without the `fyne` tag, `cmd/usbwriter` is only a dependency-free informational
launcher for headless environments.

## Language

GoFlasher follows the process locale. Override it for one launch with
`GOFLASHER_LANG`:

```sh
GOFLASHER_LANG=zh-TW go run -tags fyne ./cmd/usbwriter
GOFLASHER_LANG=en go run -tags fyne ./cmd/usbwriter
```

Unsupported locales fall back to English.

## Testing

The automated suite uses temporary regular files and fake sysfs/procfs trees;
it does not write to real block devices.

```sh
go test ./...
```

See **[TESTING.md](TESTING.md)** for race tests, GUI checks, package smoke tests,
and the deliberately guarded real-device test command.

## Security and safety

Read **[SECURITY.md](SECURITY.md)** before reporting a vulnerability. Do not
publish a device-selection or raw-write vulnerability in a public issue.

GoFlasher is free software distributed under the **GNU General Public License
version 3**. See [LICENSE](LICENSE).

## Enhancements and bugs

Use the [GitHub issue tracker](https://github.com/KelenHappy/goflasher/issues) for
non-sensitive bug reports and feature requests. Include the GoFlasher version,
Linux distribution, desktop environment, and relevant redacted log entries.
