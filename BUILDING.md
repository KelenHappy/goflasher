# Building GoFlasher

## Supported build targets

The raw-device backend and Fyne GUI build on Linux, Windows, and macOS. Windows uses
PowerShell storage cmdlets for conservative removable-USB discovery and taking
the target disk offline; run the Windows GUI as Administrator for raw access.
macOS uses `diskutil` for removable-USB discovery, unmount, and eject operations.

The reusable `internal/disk` abstraction is separate from the existing writer
backends. Its common `Manager` API contains no native handles or platform
constants. `disk.NewManager()` is selected by build tags. The Linux manager is
implemented with sysfs, mountinfo, swap inspection, and udisks2. Windows and
macOS intentionally return `disk.ErrUnsupported` from compile-safe outlines;
their future implementations will use Win32 and Disk Arbitration/IOKit without
changing callers. This keeps the current work focused on making Linux reliable.

## Requirements

- The Go toolchain version declared by `go.mod` (currently Go 1.26.4).
- A C compiler and the Linux development libraries required by Fyne.
- The UDisks2 system service at runtime for unmount and device power-off
  operations; GoFlasher calls it directly over D-Bus and does not invoke
  `udisksctl`.
- polkit/`pkexec` at runtime for narrowly scoped raw-device operations.
- No portal, D-Bus file chooser, `kdialog`, or Zenity package is needed; the
  Linux image chooser is part of the Fyne GUI.

On Ubuntu 24.04, install the build and runtime dependencies with:

```sh
sudo apt update
sudo apt install \
  gcc libgl1-mesa-dev xorg-dev libxkbcommon-dev libwayland-dev \
  udisks2
```

Other distributions may use different package names. GoFlasher does not use
`xdg-desktop-portal` for image selection.

## Native API and command dependencies

Building successfully does not mean that every backend is free of host command
dependencies. The current runtime boundary is:

| Host | Device management | Remaining commands | Status |
|---|---|---|---|
| Linux | sysfs, procfs, `/run/udev/data`, and the UDisks2 system D-Bus API | `pkexec` starts the narrowly scoped privileged helper | GUI and backend are active |
| Windows | PowerShell Storage and CIM cmdlets | `powershell.exe` | GUI and backend are active; run as Administrator |
| macOS | `diskutil` plist output and raw `/dev/rdisk*` access | `diskutil` and `plutil`; `osascript` opens the native file chooser | GUI and backend are active; raw access requires elevation |

The commands listed for Windows and macOS are part of those operating systems;
end users do not install Visual Studio, an SDK, PowerShell, `diskutil`, `plutil`,
or `osascript` to run a packaged GoFlasher binary. Source builds are different:
the Go and Fyne/CGo build toolchains are required on the build machine, but they
are not runtime dependencies and should not be bundled into the application.

UDisks2 D-Bus removes the Linux `udisksctl` client process, not the UDisks2
daemon or its polkit authorization policy. Likewise, the Windows and macOS
backends currently compile and run on their native hosts but are not yet native
API-only implementations. Replacing the remaining commands requires separate
platform work: SetupAPI/Configuration Manager and Virtual Disk/volume-control
APIs on Windows, and Disk Arbitration plus IOKit on macOS. Such replacements
must preserve the existing repeated identity, system-disk, mount, capacity, and
removability checks before raw access.

The three-platform CI matrix proves that each native source set compiles and its
isolated tests pass. It does not prove destructive access on real hardware; the
physical-media acceptance gate in `TESTING.md` remains mandatory for releases.

Build on (or cross-compile specifically for) every target OS and architecture;
one binary cannot run unchanged on Linux, Windows, and macOS. Fyne's desktop
build uses CGo, so a native build environment is the simplest supported route.
Gzip and XZ streaming decoders are pure Go and compiled into the application;
no external decompressor is required at runtime. Public Windows releases should
be code-signed; public macOS app bundles should be code-signed and notarized.

## Build from source

Run the headless tests first:

```sh
go test ./...
```

Build the Linux GUI:

```sh
go build -trimpath -tags fyne -o dist/goflasher ./cmd/usbwriter
go build -trimpath -o dist/goflasher-helper ./cmd/goflasher-helper
sudo install -m 0755 dist/goflasher-helper /usr/libexec/goflasher-helper
sudo install -m 0644 packaging/org.goflasher.usbwriter.policy \
  /usr/share/polkit-1/actions/org.goflasher.usbwriter.policy
```

Or run it directly during development:

```sh
go run -tags fyne ./cmd/usbwriter
```

Do not build or run the GUI as root.

On Windows, install Go and the C compiler required by Fyne, then build the same
GUI entry point from an Administrator PowerShell session:

```powershell
go test ./...
go build -trimpath -tags fyne -o dist\goflasher.exe ./cmd/usbwriter
```

Run `dist\goflasher.exe` as Administrator so that Windows permits the selected
removable disk to be taken offline and opened for raw writing. GoFlasher still
revalidates the disk identity immediately before destructive access.

On macOS, install Go and the Fyne prerequisites, then build the GUI:

```sh
go test ./...
go build -trimpath -tags fyne -o dist/goflasher ./cmd/usbwriter
```

The macOS disk-manager outline does not yet link native frameworks and returns
`disk.ErrUnsupported`. A future implementation will use Disk Arbitration and
IOKit behind `disk_darwin.go` without changing the common API.

Raw `/dev/rdiskN` access requires elevated rights. Until a dedicated privileged
helper is available, launch the locally built binary with `sudo`; GoFlasher
only lists external physical disks positively classified as removable USB
media and revalidates their physical device-tree identity before access.

## Debian package

The Debian packaging script requires `dpkg-deb`. After building the GUI binary:

```sh
packaging/make-deb.sh dist/goflasher 1.0.0 dist
```

This produces `dist/goflasher_1.0.0_<architecture>.deb`. Install it with `apt`
so its declared runtime dependencies are resolved:

```sh
sudo apt install ./dist/goflasher_1.0.0_amd64.deb
```

The Debian package installs the helper at `/usr/libexec/goflasher-helper` and
its polkit action. FAT32 formatting is implemented inside the narrowly scoped
helper and does not require `dosfstools` or execute a filesystem utility as
root. The helper is a separate non-GUI executable; never make the GUI setuid
and never run it with `sudo`.

## AppImage

The AppImage script requires executable copies of `linuxdeploy` and
`appimagetool`:

```sh
packaging/make-appimage.sh \
  dist/goflasher 1.0.0 dist \
  /path/to/linuxdeploy /path/to/appimagetool
```

The current release workflow produces an x86-64 AppImage. The script uses
`linuxdeploy` to populate the AppDir and `appimagetool` to create the final
image.

The AppImage contains the helper and policy under `usr/share/goflasher`, but
cannot safely install them from its transient mount. Extract the AppImage and
install those files to the fixed system paths shown in the README, after
auditing them. Raw access fails closed until that integration is installed.

## RPM package

The RPM packaging script requires `rpmbuild`. After building the GUI binary:

```sh
packaging/make-rpm.sh dist/goflasher 1.0.0 dist
sudo dnf install ./dist/goflasher-1.0.0-1*.x86_64.rpm
```

Like the Debian package, the RPM installs the helper and polkit policy to their
fixed system paths and declares the runtime dependencies required on Fedora,
RHEL, and compatible distributions.

## Third-party license notices

All binary distributions must ship `THIRD_PARTY_NOTICES.md` and the unmodified
license files for compiled dependencies. The Linux packaging scripts copy the
BSD 3-Clause `github.com/ulikunitz/xz` license from the exact module version used
for the build. Future Windows and macOS packaging must place the same files in
the installer, application bundle, or accompanying documentation.

## Checksums

Generate checksums only after all release artifacts are final:

```sh
(cd dist && sha256sum *.deb *.AppImage > SHA256SUMS)
```

Verify them with:

```sh
cd dist
sha256sum --check SHA256SUMS
```

## GitHub Actions releases

`.github/workflows/release.yml` runs on tags matching `v*` and can also be
started manually. It:

1. runs the headless tests;
2. builds the Linux Fyne binary;
3. builds the Debian package and AppImage;
4. generates `SHA256SUMS`;
5. uploads the files as an Actions artifact; and
6. on tag builds, attaches them to the corresponding GitHub release.

A manual workflow run creates an Actions artifact but does not publish a GitHub
release.

## Windows work remaining

The Windows GUI, native Explorer chooser, removable-device discovery, repeated
identity checks, offline operation, raw writing, and read-back verification are
implemented. A public Windows release still needs hardware-isolated tests,
signed packaging, and release jobs. The current backend requires Administrator
rights; a dedicated privileged helper would provide a better privilege boundary.

## macOS work remaining

The macOS GUI, native Finder chooser, conservative removable-USB backend, raw
writing, read-back verification, and `diskutil` eject operation are implemented.
A public macOS release still needs hardware-isolated tests, a dedicated
privileged helper, signed and notarized packaging, and release jobs.
