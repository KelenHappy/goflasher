# TODO

Open work for Linux and Windows. macOS is deferred (see the end).

## Linux

### 1. Installer build performance (high priority)

Building Windows installer media through the privileged helper is likely very
slow. Measured with a 16 GiB target (8 KiB clusters):

- `fat32.Builder` issues ~262,144 `WriteAt` calls per GiB of file data and
  writes 2.06× the payload, because `File.openCluster` zero-fills every
  cluster before `writeChunk` writes it (`internal/fat32/builder.go`).
- Each `WriteAt` is one helper round trip (`remoteInstallerSession`). For every
  command the helper runs `revalidateTarget`: sysfs resolve twice, a full
  `readBlockTopology` scan, and parsing of mountinfo and swaps
  (`internal/linux/helper_linux.go`).
- The session descriptor is opened with `O_SYNC` (`deviceOpenFlags`), so every
  8 KiB write waits for the device.
- Read-back verification also reads one cluster per call
  (`fatVolume.hashFile` in `internal/verify/installer.go`).

A ~5 GB `install.wim` means about 1.3 million round trips. Not yet timed on real
hardware.

Proposed fix (each part needs a security review):

- [ ] Write whole cluster runs from `File.Write` and zero only the unwritten
      tail of the last cluster.
- [ ] Do full revalidation when the session opens and at a bounded interval
      (time or bytes); check each command with `validateDescriptor` (fstat
      rdev) only.
- [ ] Drop `O_SYNC` for `modeInstallerSession`; durability comes from
      `SessionFlush`.
- [ ] Read several clusters per call in `hashFile` when the chain is
      contiguous.
- [ ] Time a full build on physical media before and after.

### 2. Shipping the installer builder

- [ ] Linux packages offer the builder only when `LIBWIM_LIBRARY` is bundled.
      wimlib is still marked "NOT YET CLEARED FOR RELEASE"
      (`docs/legal/THIRD_PARTY_NOTICES.md`, `docs/legal/RELEASE-COMPLIANCE.md`).
      Without it the builder refuses before touching the disk.

### 3. FAT32 format sector-size check

- [ ] `formatFAT32` in the helper always writes a 512-byte-sector layout but
      checks `queue/logical_block_size` only for installer sessions. Refuse
      non-512 devices for formatting too. Risk is low: admitted devices are
      flash drives and card readers.

## Linux and Windows

- [ ] **Progress while staging and splitting the WIM.** Copying `install.wim`
      to the temporary directory (`stageWIM`) and the DISM or libwim split
      report only a stage, with no byte progress. This needs a progress
      callback through `wimPreparer.PrepareWithProgress`.
- [ ] **Hotplug.** The device list refreshes only when the user presses Rescan.
      Consider udev/UDisks2 signals on Linux and `WM_DEVICECHANGE` on Windows.
- [ ] **GUI tests under `-race`.** Four `cmd/usbwriter` tests already fail with
      `-race` before any of the recent changes, because Fyne's test driver runs
      `fyne.Do` inline and does not serialize it. CI runs these tests without
      `-race`. Fix them or document why race checking is skipped here.
- [ ] `gofmt` reports `internal/legal/legal.go`.

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

## Deferred: macOS

- Privileged helper (XPC, SMAppService, signing, notarization): Phases 5–7 in
  `docs/architecture/MACOS-CONVERGENCE.md`.
- `OpenInstallerTarget` returns the raw `/dev/rdisk*` file directly. Raw disk
  nodes need sector-aligned I/O, so wrap it with `internal/blockio` the same
  way as on Windows.
- `go vet` warns about `unsafe.Pointer` in
  `internal/disk/darwin/native/library.go`.
