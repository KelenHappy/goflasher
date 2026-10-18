# TODO

Open work for Linux and Windows. macOS is deferred (see the end). Checks
that need physical hardware are not tracked here.

## Open a PR to confirm CI

The changes below pass locally (Windows: `go test -race ./...`, `go vet`,
`build.sh`/`verify.sh`, the release packaging and legal gate; Linux: every
package's tests on a real kernel under WSL Fedora 44). These need a CI run:

- [ ] `wimlib.yml`: the new Windows job (`setup-winlibs` with the pinned
      WinLibs `.7z` SHA-256 and `choco install pkgconfiglite --version 0.28`,
      then `build.sh`/`verify.sh` on `windows-latest`), and the existing Linux
      and macOS jobs. Check that CI's windows-amd64 `libwim.a` hash matches
      the recorded `2831866f…6db721`, and that `ZERO_AR_DATE` makes the macOS
      archives reproducible under Xcode 16.4.
- [ ] Add the linux-amd64, macos-amd64 and macos-arm64 `libwim.a` hashes
      printed by those jobs to `docs/legal/wimlib-1.14.5/artifact.sha256` and
      `build-metadata.txt`. Until then `check-wimlib-archive.sh` refuses them.
- [ ] `ci.yml`: the GUI tests now run with `-race` on Windows as well as
      macOS; confirm the `windows-latest` runner's MinGW passes.

## Turning the installer builder on in releases

- [ ] Set the repository variables `WIMLIB_COMPLIANCE_RECORD_PATH` to
      `docs/legal/wimlib-1.14.5`, and `WINDOWS_BUILDER_READY` (and, once their
      hashes are recorded, `LINUX_WINDOWS_BUILDER_READY` /
      `MACOS_WINDOWS_BUILDER_READY`) to `true`. Until then every release is
      raw-writer-only; Windows used DISM before.

The record (`docs/legal/wimlib-1.14.5`) is approved: source and archive
hashes, build metadata, dependency report, license inventory (dual
GPL-3.0-or-later / LGPL-2.1-or-later, MIT files inside) with the license
texts, Corresponding Source and relinking instructions, and `LEGAL_APPROVED`.
Packages ship it beside the executable (the Windows ZIP under
`legal/wimlib-1.14.5`, staged by `packaging/windows --wimlib-record`), and the
publish job attaches the wimlib source archive.

## Linux

- [ ] Optional hardening from the helper security review: open the installer
      session descriptor with `O_EXCL`. The kernel would then refuse mounts and
      swapon on the target for the whole session, instead of the bounded full
      revalidation (1 s / 64 MiB) catching them afterwards. Needs a check that
      nothing on a desktop (udisks, blkid) holds an exclusive claim that would
      make the open fail with `EBUSY`.

Review outcome for the bounded revalidation and the dropped `O_SYNC`: every
command still checks the descriptor's rdev, and a replaced device leaves the
open descriptor dead rather than redirected, so the window only delays
noticing a mount or swap on the target itself; `SessionFlush` stays the
durability point. The review found that `SessionFlush` did not invalidate the
block cache, so installer read-back verification (a second session on the
same device) could read cached pages instead of the media. It now syncs and
then issues `BLKFLSBUF`, like the raw writer's flush.

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
