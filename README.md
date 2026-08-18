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

## Build
See [BUILDING.md](docs/development/BUILDING.md#native-api-and-command-dependencies) for
the exact boundary and the native-API migration direction.

## Install

On Linux, GoFlasher always uses its bundled Fyne image chooser. Image selection
does not call XDG Desktop Portal, D-Bus, `kdialog`, Zenity, Dolphin, or Nautilus,
so it requires no desktop-specific package. The chooser title, Choose button,
and Cancel button use GoFlasher's English or Traditional Chinese localization.

The Debian, RPM, and Arch Linux packages install the privileged helper and the
named GoFlasher polkit action to their fixed root-owned system paths; no
separate helper installation is required.

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

Use the [GitHub issue tracker](https://github.com/KelenHappy/goflasher/issues) for
non-sensitive bug reports and feature requests. Include the GoFlasher version,
Linux distribution, desktop environment, and relevant redacted log entries.
