# GoFlasher hardware acceptance specification v1

[Traditional Chinese / 繁體中文版](spec-v1.zh-TW.md)

**Protocol identifier:** `goflasher-hwtest/v1`  
**Status:** required for every stable release candidate  
**Scope:** Linux, Windows, and macOS production backends on physical hardware

## Safety gates

1. Tests use media owned by the tester, empty of needed data, and explicitly
   designated disposable. Never attach a disk containing the OS, home data, or
   the only copy of any data.
2. A reviewer prepares a JSON allowlist from `allowlist.example.json`. Every
   entry must give the exact backend identity, exact capacity, exact model,
   serial when available, and `"disposable": true`. Store the reviewed file
   with the release evidence.
3. Every invocation requires `--allowlist`. Every device-specific invocation
   requires `--device-id`; the harness never chooses the first enumerated disk.
   A path, drive letter, or disk number is not accepted as selection input.
4. Before **each destructive case**, run `--case prepare`. Read the displayed
   identity and physical label, then pass its complete `ERASE <identity>
   <random-nonce>` string through `--confirmation`. The challenge expires after
   15 minutes and its state file is deleted before I/O, so it cannot be reused.
5. Use an incompressible, deterministic 256 MiB v1 image whose first four bytes
   are not `00 ff 47 46`; record its byte length and SHA-256 in the evidence.
   For removal/cancellation use a second deterministic image of at least 4 GiB
   so the intervention can occur before completion.

## Common commands

Build the harness from the exact release-candidate commit:

```sh
go build -trimpath -o usbwriter-hwtest ./cmd/usbwriter-hwtest
./usbwriter-hwtest --allowlist approved-devices.json --case inventory
```

Prepare a destructive run (repeat immediately before every destructive case):

```sh
./usbwriter-hwtest --allowlist approved-devices.json \
  --device-id 'EXACT_ID' --case prepare --challenge-file challenge.json
# Copy the entire emitted string; do not script or derive it.
```

Then add `--challenge-file challenge.json --confirmation 'ERASE …'` to the
destructive case command. A PASS line is necessary but not sufficient evidence;
retain the complete stdout/stderr and the platform evidence listed below.

## Required cases on each platform

| ID | Procedure | Required result |
|---|---|---|
| HW-01 | Inventory with no target, insert allowlisted media, run `enumerate-present`; unplug it and run `enumerate-absent`; reinsert in the same physical port and run `enumerate-present`. | Same backend identity returns; absence is observed; no device is implicitly selected. Also document a different-port insertion and its expected platform-specific identity outcome. |
| HW-02 | Save the first `enumerate-present` snapshot. Remove it, attach a different allowlisted disposable device so the OS reuses the Linux/macOS node or Windows disk number, then run `path-reuse`. | Different identity at reused address is reported and the old identity is not selected. Repeat until reuse is actually observed. |
| HW-03 | Create and mount a partition on disposable media, then perform `write-verify-eject`. | All target partitions are unmounted before opening; no unrelated mount changes; write succeeds. |
| HW-04 | With test media removed, capture GUI/harness inventory plus platform disk metadata showing the boot/system disk and active swap/pagefile. | System disk and swap device are absent from the allowed inventory. No destructive command is run against them. |
| HW-05 | Run `write-cancel` with the large image and a deadline that fires during writing. | Non-zero/cancel result, no flush/verify/eject success, GUI remains responsive, partial media remains disposable. |
| HW-06 | Run `write-remove` and physically unplug only the disposable target after writing progress begins. | I/O fails closed; no completion or verification is reported; reinsertion requires fresh enumeration and confirmation. |
| HW-07 | Run `write-verify-eject` with the v1 image. | Unmount, complete write, explicit flush, byte-count-limited read-back SHA-256 verification, and platform eject/offline all succeed. |
| HW-08 | Run `corruption-detect`; it writes/verifies, alters the first four bytes through the backend, flushes, then reads back against the original SHA-256. | `PASS: deliberate corruption detected`; a mismatch can never be reported as verified. |
| HW-09 | After HW-07 eject, inspect platform state and attempt safe physical removal. | Linux/macOS reports ejected or powered off; Windows native safe removal succeeds; no mounted partitions remain. |

`write-remove` intentionally prints its unplug instruction before starting. If
the image completes first, the case fails and must be repeated with a larger
image. Cancellation and removal are separate cases.

## Platform evidence commands

Record these before and after relevant cases (redact unrelated serials only in
public copies; preserve unredacted evidence in the restricted release archive):

### Linux

```sh
uname -a; cat /etc/os-release
lsblk -b -O -J
findmnt --json
cat /proc/swaps
journalctl --since '10 minutes ago' -u polkit --no-pager
```

Confirm polkit prompts name GoFlasher, the GUI UID is not 0, the helper is
root-owned, and `/usr/libexec/goflasher-helper` is not setuid.

### Windows

```powershell
Get-ComputerInfo | Select-Object WindowsProductName,WindowsVersion,OsBuildNumber
Get-Disk | Format-List Number,FriendlyName,SerialNumber,UniqueId,Size,BusType,IsBoot,IsSystem,IsOffline
Get-Partition | Format-List DiskNumber,PartitionNumber,DriveLetter,IsBoot,IsSystem
Get-Volume | Format-List DriveLetter,FileSystemLabel,Size
```

These commands are optional test-evidence collection only; the production
backend never invokes PowerShell. Record the UAC/admin context and show that
only the chosen disk is locked, dismounted, and safely removed.

### macOS

```sh
sw_vers
diskutil list -plist > diskutil-list-before.plist
diskutil info -plist /dev/diskN > target-info-before.plist
mount
sysctl vm.swapusage
```

Record the corresponding post-case plist and the `diskutil eject` outcome.

## Acceptance

All HW-01 through HW-09 must pass independently on Linux, Windows, and macOS
using the release-candidate binaries and approved matrix in `docs/development/TESTING.md`. A
failure, skipped case, address reuse that was not actually observed, missing
evidence, or test performed only in a VM blocks a stable release. Tests may be
marked not applicable only by a documented security review that updates this
versioned specification.
