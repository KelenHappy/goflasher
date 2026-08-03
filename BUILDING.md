# Building GoFlasher

## Supported build targets

The raw-device backend and Fyne GUI build on Linux, Windows, and macOS. Windows uses
PowerShell storage cmdlets for conservative removable-USB discovery and taking
the target disk offline; run the Windows GUI as Administrator for raw access.
macOS uses `diskutil` for removable-USB discovery, unmount, and eject operations.

## Requirements

- The Go toolchain version declared by `go.mod` (currently Go 1.26.4).
- A C compiler and the Linux development libraries required by Fyne.
- `xz` at runtime when opening XZ-compressed images.
- `udisksctl` at runtime for unmount and device power-off operations.
- polkit/`pkexec` at runtime for narrowly scoped raw-device operations.
- XDG Desktop Portal and a desktop-specific portal backend for native image
  selection.

On Ubuntu 24.04, install the build and runtime dependencies with:

```sh
sudo apt update
sudo apt install \
  gcc libgl1-mesa-dev xorg-dev libxkbcommon-dev libwayland-dev \
  udisks2 xdg-desktop-portal xz-utils
```

Other distributions may use different package names.

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
its polkit action. The helper is a separate non-GUI executable; never make the
GUI setuid and never run it with `sudo`.

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
