# TODO

Open work for Linux and Windows. macOS is deferred (see the end).

## Linux

### 1. Installer build performance

The round-trip fixes are in: `fat32.File` writes whole contiguous cluster runs
and zeroes only the unwritten tail of a run's last cluster, the helper fully
revalidates at most once per second or 64 MiB and checks the descriptor
(fstat rdev) for every command, the session descriptor is no longer `O_SYNC`
(the helper syncs every 32 MiB and on `SessionFlush`), and `hashFile` reads
contiguous clusters together (up to 1 MiB). With the executor's 1 MiB copy
buffer, a ~5 GB `install.wim` now takes about 5,000 helper round trips instead
of about 1.3 million.

- [ ] Security review of the helper changes in `internal/linux/helper_linux.go`:
      the bounded full revalidation (`fullRevalidationInterval`,
      `fullRevalidationBytes`) and the dropped `O_SYNC`
      (`unsyncedWriteLimit`).
- [ ] Time a full build on physical media and compare it with a build from
      before these changes.

### 2. Shipping the installer builder

- [ ] wimlib is linked statically into the GUI (`wimlib` build tag), and the
      Linux and macOS release jobs build it when `LINUX_WINDOWS_BUILDER_READY`
      or `MACOS_WINDOWS_BUILDER_READY` is true. What is still missing is the
      compliance record at `WIMLIB_COMPLIANCE_RECORD_PATH`: the approved
      `libwim.a` hash, dependency report, Corresponding Source location and
      legal approval. wimlib is still marked "NOT YET CLEARED FOR RELEASE"
      (`docs/legal/THIRD_PARTY_NOTICES.md`, `docs/legal/RELEASE-COMPLIANCE.md`).
- [ ] The static wimlib build is verified only on Linux (Ubuntu 24.04
      container). Confirm that the macOS jobs in `wimlib.yml` pass and that
      `ZERO_AR_DATE` makes `libwim.a` reproducible under Xcode 16.4.
      `verify.sh` now also requires `wimlib_register_progress_function`, and
      `wim-smoke` checks that split progress reaches its total.

## Linux and Windows

- [ ] **Hotplug on real hardware.** The GUI rescans 750 ms after the backend
      reports a change, and defers the rescan while an operation runs. Linux
      follows UDisks2 `InterfacesAdded`/`InterfacesRemoved` (checked against
      the system bus with a loop device only); Windows uses
      `CM_Register_Notification` for the disk interface class, which has not
      run yet. Plug and unplug a stick on both, including during a write.
- [ ] **DISM progress on real hardware.** The split progress parser expects
      DISM's `[====  25.0%  ]` bar on redirected output. Confirm the bar is
      printed when stdout is a pipe; otherwise the split stage shows only its
      start and end.

## Windows

- [ ] **Hardware validation of the new paths** with
      `docs/development/HARDWARE-TESTING.md`:
  - installer builder (`OpenInstallerTarget`/`OpenInstallerReader`)
  - DISM split of an `install.wim` larger than 4 GiB
  - raw write and read-back of an image whose size is not a multiple of 512
- [ ] Confirm that a 4Kn USB disk is refused for installer builds
      (`ErrUnsupportedSectorSize`).
- [ ] Check raw-write throughput: `blockio.Aligned` forwards at most 1 MiB per
      request, where the old path issued 4 MiB writes.
- [ ] The GUI tests run without `-race` on Windows CI (macOS runs them with
      it). Enable it if the runner's cgo toolchain supports the race detector.

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
