# Building GoFlasher

[Traditional Chinese / 繁體中文版](BUILDING.zh-TW.md)

The isolated native macOS proof of concept and its CI validation boundary are
documented in [docs/MACOS-NATIVE-PHASE1.md](../architecture/MACOS-NATIVE-PHASE1.md).

## Supported build targets

The raw-device backend and Fyne GUI build on Linux, Windows, and macOS. Windows uses
native Win32 SetupAPI, Configuration Manager, volume-control, and storage IOCTL
calls; run the Windows GUI as Administrator for raw access.
macOS uses Disk Arbitration and IOKit for discovery, identity, mount inspection, unmount, and eject, and AppKit for image selection.

The reusable `internal/disk` abstraction is separate from the existing writer
backends. Its common `Manager` API contains no native handles or platform
constants. `disk.NewManager()` is selected by build tags. The Linux manager is
implemented with sysfs, mountinfo, swap inspection, and udisks2. Windows uses
the native Win32 backend. macOS implements the same manager with nested PureGo bindings; native handles never leave the nested package.

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
| Windows | SetupAPI, cfgmgr32, `DeviceIoControl`, volume FSCTLs, and raw `\\.\PhysicalDriveN` access | None for disk operations | GUI and backend are active; run as Administrator |
| macOS | Disk Arbitration and IOKit; AppKit `NSOpenPanel`; migration raw `/dev/rdisk*` writer | None for discovery, lifecycle, or picker | Native manager active; privileged raw-operation cutover remains gated |

The Windows and macOS discovery/lifecycle implementations have no CLI runtime
dependency. Windows deliberately uses one elevated GUI process: the production
executable carries a UAC `requireAdministrator` manifest, and discovery, locking,
raw I/O, formatting, and eject all run in that process. There is no Windows
privileged helper, service, or cross-process IPC, and the backend never invokes
PowerShell or another command interpreter. This platform-specific model does not
change Linux: its narrowly scoped helper and polkit boundary remain in place.
UDisks2 D-Bus removes the Linux `udisksctl` client process, not the UDisks2 daemon
or its polkit policy. Native replacements must preserve repeated identity,
system-disk, mount, capacity, and removability checks before raw access.

The three-platform CI matrix proves that each native source set compiles and its
isolated tests pass. It does not prove destructive access on real hardware; the
physical-media acceptance gate in `TESTING.md` remains mandatory for releases.

### Windows-host installer builder boundary

On Windows the installer builder runs inside the same elevated process as the
raw writer. After every volume on the disk is locked and dismounted, it opens
the identity-checked `\\.\PhysicalDriveN` handle, writes the GPT and FAT32
structures itself, and reads them back through an independent handle for
semantic verification. Raw disk handles accept only whole-sector requests, so
all builder and verifier I/O passes through `internal/blockio`, which completes
partial sectors with read-modify-write inside the validated disk size. Disks
whose logical sector size is not 512 bytes are refused. No command runs while
building media: an `install.wim` larger than the FAT32 file limit is split in
process by the statically linked wimlib (see "WIM splitting" below).

### macOS Windows-installer builder boundary

The macOS installer builder is a **development-only migration path**, not a
production-ready feature. It reuses Disk Arbitration/IOKit identity refresh,
unmount/eject, an exclusive identity-bound raw descriptor, and an in-process
partition-bounded FAT32 view; it never invokes `diskutil`, `hdiutil`, `mount`,
`mkfs.fat`, or a WIM command-line program while building media. Public releases
must not advertise or enable this builder until destructive raw access is owned
by an authenticated helper/XPC protocol and the physical-media gate passes.

Installer-capable app bundles link the pinned wimlib statically into the GUI
executable (see "WIM splitting" below); no libwim file ships
in the bundle. A GUI built without the `wimlib` tag is raw-writer-only and WIM
preflight fails closed.
Code signing alone never enables the builder: public macOS workflows link
wimlib in only when the independent `MACOS_WINDOWS_BUILDER_READY` approval gate
is true. Until authenticated helper/XPC access and physical-media acceptance are
approved, signed releases remain raw-writer-only.

### WIM splitting

Every platform splits `install.wim` with wimlib 1.14.5, linked statically into
the GUI executable through cgo when it is built with the `wimlib` build tag
(`internal/wim/native/link_wimlib.go`). The application never loads a libwim
file at run time. Without the tag, `internal/wim/native` compiles a stub and the
installer builder reports itself unavailable, so `go test ./...` and ordinary
development builds need no wimlib.

The native build recipe is locked in `packaging/wimlib/BUILD.lock`: wimlib
1.14.5, Ubuntu 24.04/Clang 18, Xcode 16.4 Apple Clang, or WinLibs GCC 16.2.0
UCRT r2, and the recorded configure flags, which build only the static
`libwim.a`. The archive is reproducible: repeated builds from the same lock
produce the same SHA-256. On Windows, `build.sh` runs under Git Bash and needs
WinLibs' `gcc` and `mingw32-make` plus `pkg-config` on `PATH`
(`.github/actions/setup-winlibs` installs the pinned set in CI); the GUI must
then be built with the same WinLibs `gcc` so both share the UCRT C runtime.
CI builds Linux amd64, both macOS architectures, and Windows amd64 (Linux arm64
is not currently a release target), then checks the archive SHA-256 and symbols, runs
the `wimlib`-tagged `internal/wim` tests, and links and runs a real open/split
smoke fixture that must not depend on a shared libwim.

To build an installer-capable GUI locally (on the pinned toolchain):

```sh
src=$(packaging/wimlib/fetch.sh /tmp/wimlib-src)
packaging/wimlib/build.sh "$src" /tmp/wimlib
CGO_CFLAGS=-I/tmp/wimlib/include CGO_LDFLAGS=-L/tmp/wimlib/lib \
  go build -trimpath -tags fyne,no_emoji,wimlib -o dist/goflasher ./cmd/usbwriter
```

Release workflows do this only when `LINUX_WINDOWS_BUILDER_READY`,
`MACOS_WINDOWS_BUILDER_READY` or `WINDOWS_BUILDER_READY` is true. They then
require the repository variable `WIMLIB_COMPLIANCE_RECORD_PATH` (a path inside
the checkout; the record is `docs/legal/wimlib-1.14.5`),
refuse an archive whose SHA-256 is not listed in that record's
`artifact.sha256`, and install the record next to the application. The packaging
scripts detect a wimlib-linked executable with
`packaging/legal/links-wimlib.sh` and refuse to package it without the record
(the Windows portable ZIP stores it under `legal/wimlib-1.14.5`), and the
release attaches the wimlib source archive.

Build on (or cross-compile specifically for) every target OS and architecture;
one binary cannot run unchanged on Linux, Windows, and macOS. Fyne's desktop
build uses CGo, so a native build environment is the simplest supported route.
Gzip and XZ streaming decoders are pure Go and compiled into the application;
no external decompressor is required at runtime. Public Windows executables must
be Authenticode signed; public macOS app bundles must be code-signed and notarized.

## Build from source

Run the headless tests first:

```sh
go test ./...
```

Build the Linux GUI:

```sh
go build -trimpath -tags fyne,no_emoji -o dist/goflasher ./cmd/usbwriter
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
GUI entry point from an Administrator Command Prompt:

```bat
go test ./...
go build -trimpath -tags fyne,no_emoji -ldflags "-H=windowsgui" -o dist\goflasher.exe ./cmd/usbwriter
```

Run `dist\goflasher.exe` as Administrator so that Windows permits the selected
removable disk to be taken offline and opened for raw writing. GoFlasher still
revalidates the disk identity immediately before destructive access.

## Windows portable distribution

**Windows distribution is portable-only.** Windows v1 supports amd64 and is
built on GitHub's `windows-latest` runner. The permanent user flow is:

```text
Download → Extract ZIP → Run GoFlasher.exe as Administrator
```

There is no Windows installer, service, background updater, registry setup,
shell extension, driver package, or automatic update mechanism. Users download
future versions manually from the official GitHub Releases page. The portable
asset and its checksum are:

```text
GoFlasher-${VERSION}-windows-amd64.zip
GoFlasher-${VERSION}-windows-amd64.zip.sha256
```

Production ZIPs contain the signed `GoFlasher.exe`, `README-Windows.txt`, English and
Traditional Chinese third-party notices, and `licenses/` with unmodified license
files for compiled Go modules. It contains no signing certificate, signing
temporary files, build cache, source tree, or debug symbols.

The platform-neutral Go command in `packaging/windows/make-portable.go` creates
the layout and checksum from an
already-built executable. The release workflow embeds the UAC
`requireAdministrator` manifest, signs with SHA-256 plus an RFC 3161 timestamp,
verifies the Authenticode signature, and only then creates the ZIP. Signing is
enabled only when the `WINDOWS_PRODUCTION_READY` GitHub Actions repository
variable is exactly `true`; otherwise, development and alpha workflows package
the executable unsigned. Before enabling that variable, repository maintainers
configure these GitHub Actions secrets:

- `WINDOWS_CERTIFICATE_PFX_BASE64`: base64-encoded public code-signing PFX;
- `WINDOWS_CERTIFICATE_PASSWORD`: its password.

The PFX exists only in the runner temporary directory and a shell `trap` deletes
it on every exit path; it is never copied into the portable directory or uploaded.

### PowerShell usage audit

The application, Windows backend, portable packager, CI package validation, and
release/signing workflow do not invoke PowerShell. The portable packager is a Go
command, workflow orchestration uses Git Bash plus Windows SDK tools, source-build
examples use Command Prompt, and user checksum instructions use `certutil`.

The only remaining PowerShell command examples are in the hardware-test
specification. They are optional maintainer evidence collection using Windows
inventory cmdlets (`Get-Disk`, `Get-Partition`, and `Get-Volume`); they are not
executed by GoFlasher, its package, CI, or the release workflow. Replacing those
cmdlets with less complete deprecated inventory commands would reduce evidence
quality without removing a product dependency, because no such dependency exists.

On macOS, install Go and the Fyne prerequisites, then build the GUI:

```sh
go test ./...
go build -trimpath -tags fyne,no_emoji -o dist/goflasher ./cmd/usbwriter
```

The Darwin manager loads Disk Arbitration and IOKit through PureGo. The image picker uses AppKit `NSOpenPanel`; neither path invokes a command-line fallback. Never run the GUI with `sudo`. Raw-operation development remains fail-closed until the authenticated helper/XPC cutover is complete.

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

For Git tags containing underscores, the packaging script replaces each
underscore with a dot in the Debian version and output filename because Debian
version fields do not permit underscores.

The Debian package installs the helper at `/usr/libexec/goflasher-helper` and
its polkit action. FAT32 formatting is implemented inside the narrowly scoped
helper and does not require `dosfstools` or execute a filesystem utility as
root. The helper is a separate non-GUI executable; never make the GUI setuid
and never run it with `sudo`.

## RPM package

The RPM packaging script requires `rpmbuild`. After building the GUI binary:

```sh
packaging/make-rpm.sh dist/goflasher 1.0.0 dist
sudo dnf install ./dist/goflasher-1.0.0-1*.x86_64.rpm
```

Like the Debian package, the RPM installs the helper and polkit policy to their
fixed system paths and declares the runtime dependencies required on Fedora,
RHEL, and compatible distributions.

## Arch Linux package

The release workflow also produces an x86-64 native pacman package:

```sh
packaging/make-arch.sh dist/goflasher 1.0.0 dist
sudo pacman -U ./dist/goflasher-1.0.0-1-x86_64.pkg.tar.zst
```

It carries the same GUI, root-owned helper, polkit policy, desktop metadata,
notices, and dependency license as the Debian and RPM packages. Arch Linux is
an additional package target; it does not change the Linux privilege boundary.

## Third-party license notices

All binary distributions must ship `THIRD_PARTY_NOTICES.md` and the unmodified
license files for compiled dependencies. The Linux packaging scripts copy the
BSD 3-Clause `github.com/ulikunitz/xz` license from the exact module version used
for the build. Windows packaging copies notices and compiled-module licenses into
the portable ZIP; macOS packaging places applicable files in the application
bundle or accompanying documentation.

## Checksums

Generate checksums only after all release artifacts are final:

```sh
(cd dist && sha256sum *.deb *.rpm *.pkg.tar.zst > SHA256SUMS)
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
3. builds the Debian, RPM, Arch Linux, and Windows amd64 portable ZIP assets
   (Windows support is still under development, and the executable is not yet
   Authenticode signed);
4. generates platform checksums;
5. uploads each platform's files as Actions artifacts; and
6. on tag builds, attaches them to the same GitHub release.

A manual workflow run creates an Actions artifact but does not publish a GitHub
release.

## Windows release boundary

The Windows GUI, native Explorer chooser, removable-device discovery, repeated
identity checks, volume lock/dismount, raw writing, and read-back verification are
implemented. Native in-process FAT32 formatting, volume locking/dismount, flush,
and safe eject are also implemented without PowerShell. The release workflow
produces the signed amd64 portable ZIP and checksum. Hardware-isolated acceptance
of the exact release candidate remains mandatory. The supported Windows privilege
architecture is the single elevated GUI process described above, not a helper or
service.

## macOS release boundary

Native discovery, operation-lifetime identity, mount inspection, unmount/eject callbacks, post-operation refresh, and the AppKit picker are implemented without `diskutil`, `plutil`, or `osascript`. The release workflow builds separate Intel and Apple Silicon DMGs and performs signing, notarization, stapling, Gatekeeper verification, and checksums. Stable publication still requires the authenticated XPC helper cutover and exact-RC hardware acceptance described in the convergence contract; the migration writer is not grounds to bypass that gate.
