<div align="center">
  <img src="packaging/org.goflasher.usbwriter.svg" width="128" height="128" alt="GoFlasher logo">

# GoFlasher: A Safety-First USB Image Writer

**GoFlasher writes raw or compressed disk images to removable USB flash media
on Linux, Windows, and macOS.**

[繁體中文說明](docs/readme/README.zh-TW.md)
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
  `disk.NewManager()`. The Linux sysfs/udisks and native Windows implementations
  are active; macOS currently retains a compile-safe outline for a future Disk
  Arbitration/IOKit manager.

GoFlasher does not inspect, identify, or restrict the operating system contained
inside an image. On every supported host it can therefore write a Linux ISO or
any other raw disk image in a supported format: `.iso`, `.img`, `.raw`,
`.iso.gz`, `.img.gz`, `.iso.xz`, or `.img.xz`.

GoFlasher is primarily an image writer. It can also erase a selected supported
USB device and create a whole-device FAT32 filesystem named `GOFLASHER`. All
three backends use the same bundled, in-process Go formatter and do not invoke
an operating-system formatting utility. It does **not** download operating-
system images, create Windows installation workarounds, create persistent
partitions, or perform bad-block tests.

## Host platform support

“Cross-platform” means the GoFlasher application and raw-device backend can run
on **Linux, Windows, and macOS hosts**. It does not mean an image is tied to its
host: GoFlasher on any of those three platforms can write a Linux ISO, or any
other supported raw disk image, to approved removable media. Windows uses its
native Explorer chooser and Win32 SetupAPI, Configuration Manager, volume-control,
and storage IOCTL APIs; raw disk access requires an Administrator session. macOS
uses its native Finder chooser and `diskutil` for discovery, unmount, and eject;
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
privilege broker. Windows disk discovery, identity validation, raw I/O, volume
lock/dismount, flush, format, and eject are native and invoke no CLI. macOS still
uses `diskutil` for device management and `osascript` for the native chooser,
but formatting is performed in process. All three native builds are exercised by CI, but Windows and macOS
still require native-host privileges and physical-media acceptance before a
release. See [BUILDING.md](docs/development/BUILDING.md#native-api-and-command-dependencies) for
the exact boundary and the native-API migration direction.

## Prebuilt downloads

Source support and prebuilt package availability are separate. The release
workflow publishes Linux packages and an Authenticode-signed Windows amd64
portable ZIP. macOS remains available as source until its signed and notarized
release gate is enabled.

A package is not a universal executable. Build and package separately for each
operating system and CPU architecture. Debian packages resolve their declared
runtime dependencies through APT, and RPM packages do so through DNF. Windows
users need an Administrator session, and macOS builds currently need
elevated raw-disk access; distributing them publicly also requires the platform's
normal code-signing (and, on macOS, notarization) process. Gzip and XZ decoding
are compiled into GoFlasher, so packaged builds do not require external
decompressor programs.

When a packaged release is published, its GitHub release assets are produced by
the repository release workflow:

- `GoFlasher-<version>-x86_64.AppImage`
- `goflasher_<version>_amd64.deb`
- `goflasher-<version>-1*.x86_64.rpm`
- `goflasher-<version>-1-x86_64.pkg.tar.zst`
- `SHA256SUMS`
- `GoFlasher-<version>-windows-amd64.zip`
- `GoFlasher-<version>-windows-amd64.zip.sha256`

**Windows distribution is portable-only:** download the ZIP, extract it, and
run `GoFlasher.exe` as Administrator. GoFlasher does not automatically update
itself; download future versions manually from GitHub Releases.

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

The AppImage includes its privileged helper and uses it directly through
`pkexec`, so no separate helper installation is required. To use the named
GoFlasher polkit action instead of the system's generic `pkexec` action, an
administrator may install the bundled policy and a stable copy of the helper:

```sh
./GoFlasher-*-x86_64.AppImage --appimage-extract
sudo install -m 0755 squashfs-root/usr/libexec/goflasher-helper /usr/libexec/goflasher-helper
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

On Arch Linux, install the native package with:

```sh
sudo pacman -U ./goflasher-*-x86_64.pkg.tar.zst
```

Do not launch GoFlasher itself with `sudo`.

## Building

GoFlasher requires the Go version declared in [`go.mod`](go.mod). Building the
Fyne GUI also requires Linux OpenGL, X11, and Wayland development packages.
See **[BUILDING.md](docs/development/BUILDING.md)** for dependency installation, source builds,
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

See **[TESTING.md](docs/development/TESTING.md)** for race tests, GUI checks, package smoke tests,
and the deliberately guarded real-device test command. Release testers can use
the step-by-step hardware manuals in
[English](docs/development/HARDWARE-TESTING.md) or
[Traditional Chinese](docs/development/HARDWARE-TESTING.zh-TW.md).

## Security and safety

Read **[SECURITY.md](SECURITY.md)** before reporting a vulnerability. Do not
publish a device-selection or raw-write vulnerability in a public issue.

GoFlasher is free software distributed under the **GNU General Public License
version 3**. See [LICENSE](LICENSE). Compiled releases also contain BSD-licensed
third-party components; their attribution and redistribution information is in
[THIRD_PARTY_NOTICES.md](docs/legal/THIRD_PARTY_NOTICES.md).

## Enhancements and bugs

Use the [GitHub issue tracker](https://github.com/goflasher/goflasher/issues) for
non-sensitive bug reports and feature requests. Include the GoFlasher version,
Linux distribution, desktop environment, and relevant redacted log entries.
