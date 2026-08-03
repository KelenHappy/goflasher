# Testing GoFlasher

## Automated suite

Run the complete headless test suite:

```sh
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
```

The tests use temporary regular files and fake sysfs/procfs trees. They must
never write to a real block device. GitHub Actions runs the suite on Linux and
Windows so platform-neutral packages remain portable; the production writer
backend and GUI release are currently Linux-only.

## GUI build check

Install the Fyne Linux dependencies described in the README, then run:

```sh
go test -tags fyne ./cmd/usbwriter
go build -tags fyne ./cmd/usbwriter
```

Choosing an image should open the desktop environment's portal-backed native
file chooser, not an embedded Fyne file browser. Check cancellation, paths
containing spaces, and every supported image suffix.

## Package smoke tests

Build packages using the release workflow or the commands in the README. On a
disposable VM, verify:

1. `sha256sum --check SHA256SUMS` succeeds.
2. The AppImage starts after `chmod +x` without installation.
3. The Debian package installs with `apt install ./goflasher_*.deb`.
4. The desktop launcher appears and opens GoFlasher as a regular user.
5. Selecting an image opens the native chooser and cancelling it changes no
   application state.

## Explicit real-device test

Real-device testing is destructive and is never part of CI. Use an empty,
disposable USB device and follow the command in the README. Verify the model,
serial, size, and `/dev` path immediately before confirming. Never use a system
disk or a device containing data you need.
