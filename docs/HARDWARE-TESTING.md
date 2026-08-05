# GoFlasher physical hardware test checklist

[Traditional Chinese / 繁體中文版](HARDWARE-TESTING.zh-TW.md)

This checklist describes only the physical equipment and actions needed for a
release hardware test. Commands, harness options, and the formal pass/fail rules
remain in [`cmd/usbwriter-hwtest/spec-v1.md`](../cmd/usbwriter-hwtest/spec-v1.md).

> **DANGER:** Testing erases the selected USB device. Never use a system disk,
> backup disk, or a device containing the only copy of any data.

## Equipment

Prepare the following for each supported platform:

- one physical Linux, Windows, or macOS computer;
- two USB flash drives that can be completely erased;
- two clearly different labels, such as `GOFLASHER TEST A` and
  `GOFLASHER TEST B`;
- a direct USB port whenever possible;
- a phone or camera for recording device insertion and removal; and
- a tester plus a second person to verify the selected device and results.

A virtual machine may be used for package installation checks, but it does not
replace a physical computer for USB hardware acceptance.

Before starting:

1. Back up the host computer.
2. Disconnect external SSDs, hard disks, memory cards, and backup drives.
3. Empty both test drives and confirm that losing all their data is acceptable.
4. Attach a physical label to each test drive.
5. Record each drive's brand, model, capacity, and serial number when present.
6. Have the second person confirm that both drives are disposable test media.

## Example images

The required coverage is based on image format, not on testing every Linux
distribution during every release. Use one example from each required row:

| Required coverage | Example | What it checks |
|---|---|---|
| Ordinary ISO | [Debian netinst ISO](https://www.debian.org/distrib/) | ISO selection, writing, read-back verification, and PC boot |
| Ordinary IMG | An uncompressed Raspberry Pi OS `.img` from the [official Raspberry Pi OS page](https://www.raspberrypi.com/software/operating-systems/) | Full-disk IMG writing, verification, and Raspberry Pi boot |
| Gzip-compressed image | An official LibreELEC Generic x86-64 `.img.gz` image from the [LibreELEC Generic download page](https://libreelec.tv/downloads/generic/) | Gzip streaming, progress, cancellation, verification, and PC boot |
| XZ-compressed image | An official Raspberry Pi OS `.img.xz` image from the [Raspberry Pi OS download directory](https://downloads.raspberrypi.com/raspios_arm64/images/) | XZ streaming, progress, cancellation, verification, and Raspberry Pi boot |
| Destructive safety image | The fixed 256 MiB raw image defined by the v1 specification | Complete write, flush, read-back, checksum, and corruption detection |
| Interruption image | The fixed 4 GiB-or-larger raw image defined by the v1 specification | Cancellation and physical removal while writing |

Download the LibreELEC and Raspberry Pi OS images in their publisher-provided
compressed form. Do not recompress or rename them. One official `.img.gz` and
one official `.img.xz` are sufficient to cover the two compressed filename
endings; every distribution does not need to be tested in both formats. Select
the current Generic x86-64 LibreELEC image and current Raspberry Pi OS arm64
image whose filenames have the exact endings shown above, and verify their
publisher-provided checksums before testing. OpenWrt is intentionally not used.

If a Raspberry Pi download is a ZIP file, extract its `.img` first; ZIP is not
one of GoFlasher's advertised input formats.

For the real-world compatibility check, use the **current amd64 Debian netinst
ISO** from the [official Debian download page](https://www.debian.org/distrib/).
It is the single default compatibility example; Puppy Linux, Rocky Linux,
Fedora, and Arch Linux do not also need to be tested for every release
candidate. Raspberry Pi OS remains the separate IMG example above.

Record the Debian image's exact filename, version, architecture, byte size,
official checksum, download date, GoFlasher version, host platform,
verification result, and boot result. Always verify Debian's published
checksum before giving the image to GoFlasher.

## Required physical checks

Complete every check on Linux, Windows, and macOS. Use the exact release
candidate that will be published.

### 1. Detect, remove, and reconnect

- Connect test drive A directly to the computer.
- Confirm that GoFlasher displays the correct label, model, capacity, serial,
  and device identity.
- Remove drive A and confirm that it disappears.
- Reconnect it to the same port and confirm that the same physical drive is
  recognized.
- Reconnect it to another port and record any identity change.

### 2. Do not confuse two drives

- Record drive A in GoFlasher, then remove it.
- Connect drive B.
- Confirm that GoFlasher never treats drive B as drive A, even if the operating
  system gives it the same device path or disk number.
- Repeat the swap until path or disk-number reuse is actually observed.

### 3. Mounted test drive

- Create or use a partition on drive A and mount it normally.
- Select drive A for the test write.
- Confirm that only partitions on drive A are unmounted.
- Confirm that no unrelated disk or mount changes.

### 4. Protect the host system disk

- Disconnect both test drives.
- Confirm that the computer's boot disk, system disk, and active swap or
  pagefile cannot be selected in GoFlasher.
- Do not attempt a destructive write to prove this check.

### 5. Cancel during a write

- Start writing a large test image to drive A.
- Cancel after visible progress begins and before writing finishes.
- Confirm that GoFlasher does not report success, verification, or safe eject.
- Keep drive A designated as disposable because it now contains partial data.

### 6. Remove during a write

- Record the labeled drive and the GoFlasher window continuously.
- Start writing a large test image to drive A.
- After visible progress begins, unplug only drive A.
- Confirm that the operation fails and is never reported as completed or
  verified.
- Reconnect drive A and confirm that it must be detected and selected again.

### 7. Complete write and verification

- Write the standard small test image to drive A.
- Enable read-back verification and safe eject or offline behavior.
- Confirm that writing and verification complete successfully.
- Confirm that the reported checksum matches the approved test-image checksum.

### 8. Detect corrupted data

- Perform the specified corruption-detection case on drive A.
- Confirm that deliberately changed data produces a verification failure.
- A checksum mismatch must never be displayed as successful verification.

### 9. Safe removal

- After a successful verified write, inspect the physical and operating-system
  state.
- Confirm that no partition on drive A remains mounted.
- On Linux or macOS, confirm that the drive is ejected or powered off.
- On Windows, confirm that the disk is offline.
- Remove the drive only after the expected safe-removal state is visible.

## Evidence to keep

For every platform, retain:

- the computer model and OS version;
- both test-drive models, capacities, hardware revisions, and connection type;
- the release-candidate version and binary hashes;
- photos or continuous video showing the labeled drive used in each removal
  test;
- complete logs for all nine checks, including failed and repeated attempts;
- a result sheet marking every check as pass or fail; and
- approval from the tester and the second reviewer.

Serial numbers and personal identifiers may be hidden in public copies, but an
unredacted copy should be kept in restricted release storage.

## Release decision

Do not call a release stable if any platform or check was skipped, a different
binary was tested, path reuse was not observed, evidence is missing, or removal,
verification, eject, or system-disk protection remains uncertain. An incomplete
run may support a clearly labeled pre-release only.
