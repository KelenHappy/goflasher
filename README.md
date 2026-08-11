<div align="center">
  <img src="packaging/org.goflasher.usbwriter.svg" width="128" height="128" alt="GoFlasher logo">

# GoFlasher: A Safety-First USB Image Writer

**GoFlasher writes raw or compressed disk images to removable USB flash media
on Linux, Windows, and macOS.**

[繁體中文說明](readme-zh-tw.md)
</div>

> [!WARNING]
> Writing an image destroys all data on the selected device. Check its model,
> serial number, capacity, and device path before confirming.

## Features

- Write `.iso`, `.img`, and `.raw` disk images.
- Stream gzip (`.gz`) and XZ (`.xz`) images with built-in Go decoders, without
  an external decompressor or an uncompressed temporary file.
- Calculate the source SHA-256 while inspecting the image and while writing it.
- Optionally read the written bytes back and compare their SHA-256 checksum.
- Show writing and verification progress, throughput, and estimated time.
- Cancel an active operation.
- Optionally power off the USB device after a successful write.
- Use the bundled, localized Fyne file chooser on Linux and native file
  choosers on Windows and macOS.
- Display the interface in English or Traditional Chinese.
- Keep a copyable, bounded activity log in the application.
- Restrict targets to removable USB flash media or card readers. On Linux,
  generic `usb-storage` media up to 128 GB is accepted when udev omits its
  flash-drive classification.
- Reject mounted critical system disks, swap devices, ATA devices, SSD/HDD
  models, storage bridges, UAS devices, and larger ambiguous USB storage.
- Revalidate device identity before unmounting and on both sides of the
  privileged raw-device boundary.
- Provide a platform-neutral `disk.Manager` abstraction; callers only construct
  `disk.NewManager()`. The Linux sysfs/udisks implementation is active, while
  Windows and macOS currently provide compile-safe outlines for later native
  Win32 and Disk Arbitration/IOKit implementations.

GoFlasher does not inspect, identify, or restrict the operating system contained
inside an image. On every supported host it can therefore write a Linux ISO or
any other raw disk image in a supported format: `.iso`, `.img`, `.raw`,
`.iso.gz`, `.img.gz`, `.iso.xz`, or `.img.xz`.

GoFlasher is primarily an image writer. It can also erase a selected supported
USB device and create a FAT32 filesystem named `GOFLASHER`. Linux creates a
whole-device FAT32 filesystem with its bundled formatter; Windows and macOS
create an MBR layout containing a FAT32 volume. It does **not** download operating-
system images, create Windows installation workarounds, create persistent
partitions, or perform bad-block tests.

## Host platform support

“Cross-platform” means the GoFlasher application and raw-device backend can run
on **Linux, Windows, and macOS hosts**. It does not mean an image is tied to its
host: GoFlasher on any of those three platforms can write a Linux ISO, or any
other supported raw disk image, to approved removable media. Windows uses its
native Explorer chooser and PowerShell storage cmdlets; raw disk access requires
an Administrator session. macOS uses its native Finder chooser and `diskutil`;
raw disk access requires elevated rights.

The Linux backend reads sysfs, procfs, and udev information. It calls the
UDisks2 service directly over the system D-Bus for unmount and power-off
operations; the `udisksctl` CLI is not required. The GUI always remains an
ordinary user process. For write, read-back, and flush it sends only a
revalidated identity, major/minor number, capacity, and fixed operation mode to
the root-owned `/usr/libexec/goflasher-helper` through `pkexec`; it never asks
the helper to open a caller-supplied path. A polkit authentication dialog may
therefore appear for each raw-device phase. Canceling it safely aborts the
operation.

This does not yet make every platform command-free. Linux reads supplementary
udev properties directly from `/run/udev/data` and only uses `pkexec` as its
privilege broker; Windows uses PowerShell Storage/CIM cmdlets; macOS uses `diskutil`, `plutil`, and
`osascript`. All three native builds are exercised by CI, but Windows and macOS
still require native-host privileges and physical-media acceptance before a
release. See [BUILDING.md](BUILDING.md#native-api-and-command-dependencies) for
the exact boundary and the native-API migration direction.

## Prebuilt downloads

Source support and prebuilt package availability are separate. The current
release workflow publishes prebuilt **Linux** artifacts only: an x86-64
AppImage, an amd64 Debian package, and an x86-64 RPM package. The
Windows and macOS implementations are available in the source tree and can be
built from source as documented in [BUILDING.md](BUILDING.md), but signed Windows
installers and signed/notarized macOS packages are not yet published.

A package is not a universal executable. Build and package separately for each
operating system and CPU architecture. Debian packages resolve their declared
runtime dependencies through APT, and RPM packages do so through DNF. Windows
users currently need an Administrator session, and macOS builds currently need
elevated raw-disk access; distributing them publicly also requires the platform's
normal code-signing (and, on macOS, notarization) process. Gzip and XZ decoding
are compiled into GoFlasher, so packaged builds do not require external
decompressor programs.

When a packaged release is published, its GitHub release assets are produced by
the repository release workflow:

- `GoFlasher-<version>-x86_64.AppImage`
- `goflasher_<version>_amd64.deb`
- `goflasher-<version>-1*.x86_64.rpm`
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

On Linux, GoFlasher always uses its bundled Fyne image chooser. Image selection
does not call XDG Desktop Portal, D-Bus, `kdialog`, Zenity, Dolphin, or Nautilus,
so it requires no desktop-specific package. The chooser title, Choose button,
and Cancel button use GoFlasher's English or Traditional Chinese localization.

An AppImage cannot install a stable root-owned polkit helper. Before its first
use, extract the AppImage and have an administrator audit and install the two
bundled integration files (or install the Debian package instead):

```sh
./GoFlasher-*-x86_64.AppImage --appimage-extract
sudo install -m 0755 squashfs-root/usr/share/goflasher/goflasher-helper /usr/libexec/goflasher-helper
sudo install -m 0644 squashfs-root/usr/share/goflasher/org.goflasher.usbwriter.policy \
  /usr/share/polkit-1/actions/org.goflasher.usbwriter.policy
```

Install the Debian package on Debian or Ubuntu:

```sh
sudo apt install ./goflasher_*_amd64.deb
```

Install the RPM package on Fedora, RHEL, or compatible distributions:

```sh
sudo dnf install ./goflasher-*.x86_64.rpm
```

Do not launch GoFlasher itself with `sudo`.

## Building

GoFlasher requires the Go version declared in [`go.mod`](go.mod). Building the
Fyne GUI also requires Linux OpenGL, X11, and Wayland development packages.
See **[BUILDING.md](BUILDING.md)** for dependency installation, source builds,
AppImage, Debian and RPM packaging, and the current Windows/macOS limitations.

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
and the deliberately guarded real-device test command. Release testers can use
the step-by-step hardware manuals in
[English](docs/HARDWARE-TESTING.md) or
[Traditional Chinese](docs/HARDWARE-TESTING.zh-TW.md).

## Security and safety

Read **[SECURITY.md](SECURITY.md)** before reporting a vulnerability. Do not
publish a device-selection or raw-write vulnerability in a public issue.

GoFlasher is free software distributed under the **GNU General Public License
version 3**. See [LICENSE](LICENSE). Compiled releases also contain BSD-licensed
third-party components; their attribution and redistribution information is in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Enhancements and bugs

Use the [GitHub issue tracker](https://github.com/goflasher/goflasher/issues) for
non-sensitive bug reports and feature requests. Include the GoFlasher version,
Linux distribution, desktop environment, and relevant redacted log entries.
