# Testing GoFlasher

[Traditional Chinese / 繁體中文版](TESTING.zh-TW.md)

## Automated suite

Run the complete headless test suite:

```sh
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
```

The tests use temporary regular files and fake sysfs/procfs trees. They must
never write to a real block device. They complement, but cannot replace, the
three-platform physical-media acceptance suite below.

XZ fixtures are produced and decoded with the compiled pure-Go implementation.
CI runs the focused XZ tests on Linux, Windows, and macOS and rejects source,
package, or workflow changes that restore an external `xz` runtime dependency:

```sh
go test ./internal/image -run XZ
```

The normal three-platform CI matrix also compiles and tests `internal/disk` on
each host OS. Linux unit tests use temporary sysfs, mountinfo, `/run/udev/data`,
and UDisks-client fixtures, including post-unmount state verification. Windows
tests exercise its native backend through injected Win32 fakes; macOS still
tests the compile-safe `disk.Manager` outline separately from its active writer
backend. The shared FAT32 formatter is tested against temporary disk images.
Linux unmount and power-off operations use the UDisks2 system D-Bus API through
the pure-Go `godbus/dbus` client. CI rejects regressions that invoke the
`udisksctl` or `udevadm` CLI from Go code.

## GUI build check

Install the Fyne Linux dependencies described in the README, then run:

```sh
go test -tags fyne ./cmd/usbwriter
go build -tags fyne ./cmd/usbwriter
```

On Linux, choosing an image should open the embedded Fyne file browser without
calling a desktop portal. Verify its title, action buttons, and file-dialog
controls in both English and Traditional Chinese. Also check cancellation,
paths containing spaces, and every supported image suffix. Windows and macOS
should continue to open their native choosers.

## Package smoke tests

Build packages using the release workflow or the commands in the README. On a
disposable VM, verify:

1. `sha256sum --check SHA256SUMS` succeeds.
2. The AppImage starts after `chmod +x` without installation.
3. The Debian package installs with `apt install ./goflasher_*.deb`.
4. The desktop launcher appears and opens GoFlasher as a regular user.
5. Selecting an image opens the bundled Fyne chooser on Linux (the native
   chooser on Windows and macOS), and cancelling it changes no application
   state.

## Versioned physical hardware acceptance

The normative destructive procedure is
[`cmd/usbwriter-hwtest/spec-v1.md`](cmd/usbwriter-hwtest/spec-v1.md). The harness
builds on Linux, Windows, and macOS and deliberately requires:

Operators should follow the step-by-step
[physical hardware test manual](docs/HARDWARE-TESTING.md), also available in
[Traditional Chinese](docs/HARDWARE-TESTING.zh-TW.md). The versioned
specification remains authoritative for pass/fail decisions.

- a reviewed `goflasher-hwtest/v1` device allowlist with exact identity, model,
  capacity, optional serial, and `disposable: true`;
- an explicit `--device-id` rather than a path, disk number, or list position;
- a fresh random `ERASE <identity> <nonce>` confirmation for every destructive
  case, consumed before the device is touched; and
- physical disposable media. VMs, loop devices, VHDs, and sparse files do not
  satisfy hardware acceptance.

Never paste the one-time confirmation until the displayed identity, physical
label, capacity, and allowlist have been checked by the operator. The harness
never auto-selects the first device. Real-device tests remain excluded from CI.

### Approved hardware matrix

Every release candidate records the actual make/model, hardware revision,
serial (restricted evidence), capacity, connection, host, OS build, and backend
binary hash in this matrix. The minimum matrix is:

| Platform | Required host and connection | Required disposable targets | Required privilege path |
|---|---|---|---|
| Linux | Physical x86-64 or arm64 host; current supported distribution; direct USB-A/C port | One positively identified USB flash drive and one second allowlisted drive for path reuse; add an allowlisted USB card reader when supported | Ordinary-user GUI plus packaged root-owned helper and polkit policy |
| Windows | Physical Windows 11 host on a supported build; direct USB-A/C port | Two removable USB drives with stable, distinct UniqueId/serial values | Packaged binary in documented Administrator/UAC context |
| macOS | Physical supported Intel or Apple-silicon Mac; direct port or documented Apple adapter | Two external removable USB drives with distinct device-tree identities | Packaged binary with documented elevation context |

For an RC, replace generic descriptions in its evidence manifest with exact
tested inventory. A hub-only run, a single target that never demonstrates
address reuse, or testing only one CPU/OS outside the supported matrix is not
approval. Additional hardware may be recorded but does not remove minimums.

### Test images

Generate images once per specification version, store them in the restricted
test artifact repository, and verify their published evidence manifest before
each run:

| Image ID | Content and size | Use | Required record |
|---|---|---|---|
| `hwtest-v1-verify-256m.raw` | Deterministic, incompressible 256 MiB; first four bytes differ from `00 ff 47 46` | write, flush, read-back, corruption, eject | Exact byte count, SHA-256, generator source/version |
| `hwtest-v1-interrupt-4g.raw` | Deterministic, incompressible, at least 4 GiB (increase if the target writes too quickly) | cancellation and removal during write | Exact byte count, SHA-256, generator source/version |

Do not use an OS installer as the canonical test image: mutable metadata and
compression make timing and corruption evidence harder to reproduce. The image
must fit every approved disposable target.

### Required behavior coverage

Run HW-01 through HW-09 from the v1 specification on **each** platform. These
cases cover unplug/reinsert and re-enumeration, Linux/macOS device-node and
Windows disk-number reuse, mounted partitions, swap/system-disk refusal,
in-flight cancellation, in-flight physical removal, flush, read-back checksum,
deliberate corruption detection, and eject/offline behavior. A PASS printed by
the harness is valid only when the corresponding before/after platform evidence
shows the expected physical state.

### Acceptance evidence retained for every RC

Create a read-only directory named for the tag and commit, for example
`goflasher-v1.2.0-rc1-<commit>/`, containing:

1. the RC commit, source archive hash, GUI/helper/harness binary hashes, package
   hashes, build log, and signing/notarization identity where applicable;
2. the reviewed allowlist and a hardware manifest containing make/model,
   revision, capacity, connection topology, host model, OS build, and tester;
3. test-image manifest with exact byte counts, SHA-256 values, and reproducible
   generator version/source;
4. timestamped, complete stdout/stderr for every HW case, including failed and
   repeated attempts, with a final result index for Linux, Windows, and macOS;
5. before/after disk inventory, mount/partition/swap state, path or disk-number
   reuse snapshot, kernel/system logs, polkit/UAC/elevation evidence, and eject
   state using the platform commands in the specification;
6. screen recording or continuous photographs proving physical insert,
   removal, target label, displayed confirmation, cancellation, authorization
   prompt, and safe removal correspond to the logged run; and
7. a signed approval by tester and independent reviewer listing every HW-01 to
   HW-09 result, deviations, redactions, and unresolved defects.

Preserve unredacted evidence in access-controlled release storage for at least
the supported lifetime of that stable release. Public evidence may redact
serial numbers and host/user identifiers, but must retain hashes, models,
capacities, OS versions, case outcomes, and reviewer approval. A redaction must
not obscure which physical target was used across a case.

## Stable release gate

A stable release is blocked until the exact release candidate has completed
and passed the full approved hardware matrix on Linux, Windows, and macOS, all
required evidence is archived, and two-person approval is recorded. Any binary
or backend change after acceptance invalidates the affected platform run.

Skipped cases, missing evidence, an unobserved path/disk-number reuse, a system
or swap disk appearing in the allowlist, unexpected mounts, verification or
flush ambiguity, or unresolved removal/eject behavior are release-blocking.
Pre-releases may document incomplete acceptance, but must not be promoted or
described as stable until all three platform gates pass.
