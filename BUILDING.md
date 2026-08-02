# Building GoFlasher

## Supported build targets

The raw-device backend and Fyne GUI currently build and run on Linux. Core
packages are kept platform-neutral and are tested by GitHub Actions on Linux
and Windows, but the repository does not yet contain a Windows raw-device
backend or a Windows GUI entry point.

## Requirements

- The Go toolchain version declared by `go.mod` (currently Go 1.26.4).
- A C compiler and the Linux development libraries required by Fyne.
- `xz` at runtime when opening XZ-compressed images.
- `udisksctl` at runtime for unmount and device power-off operations.
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
```

Or run it directly during development:

```sh
go run -tags fyne ./cmd/usbwriter
```

Do not build or run the GUI as root.

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

A Windows release needs all of the following before it can be advertised as
supported:

- a Windows implementation of the removable-device backend and its safety
  checks;
- a Windows-native implementation of `internal/filepicker.OpenImage`;
- a Windows Fyne GUI entry point that uses those implementations;
- hardware-isolated Windows tests; and
- signed Windows packaging and release jobs.

The non-Linux picker stub exists to keep Linux D-Bus imports out of Windows
builds; it is not a functional Windows file picker.
