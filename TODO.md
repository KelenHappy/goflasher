# TODO

Open work for Linux and Windows. macOS is deferred (see the end). Checks
that need physical hardware are not tracked here.

## Turning the installer builder on in releases

- [ ] Copy the linux-amd64, macos-amd64 and macos-arm64 `libwim.a` hashes
      printed by the `wimlib.yml` jobs into
      `docs/legal/wimlib-1.14.5/artifact.sha256` and `build-metadata.txt`, and
      check that the windows-amd64 job printed the recorded `2831866f…6db721`.
      Until then `check-wimlib-archive.sh` refuses the missing archives.
- [ ] Set the repository variables `WIMLIB_COMPLIANCE_RECORD_PATH` to
      `docs/legal/wimlib-1.14.5`, and `WINDOWS_BUILDER_READY` (and, once their
      hashes are recorded, `LINUX_WINDOWS_BUILDER_READY` /
      `MACOS_WINDOWS_BUILDER_READY`) to `true`. Until then every release is
      raw-writer-only.

## Linux

- [ ] Optional hardening from the helper security review: open the installer
      session descriptor with `O_EXCL`. The kernel would then refuse mounts and
      swapon on the target for the whole session, instead of the bounded full
      revalidation (1 s / 64 MiB) catching them afterwards. Needs a check that
      nothing on a desktop (udisks, blkid) holds an exclusive claim that would
      make the open fail with `EBUSY`.

## Deferred: macOS

- Privileged helper (XPC, SMAppService, signing, notarization): Phases 5–7 in
  `docs/architecture/MACOS-CONVERGENCE.md`.
- `OpenInstallerTarget` returns the raw `/dev/rdisk*` file directly. Raw disk
  nodes need sector-aligned I/O, so wrap it with `internal/blockio` the same
  way as on Windows.
- `go vet` warns about `unsafe.Pointer` in
  `internal/disk/darwin/native/library.go`.
- Hotplug: the macOS backend does not implement `device.HotplugBackend`
  (DiskArbitration appeared/disappeared callbacks would).
