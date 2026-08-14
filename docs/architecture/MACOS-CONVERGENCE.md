# macOS production convergence target

[Traditional Chinese / 繁體中文版](MACOS-CONVERGENCE.zh-TW.md)

This document is the binding architecture, migration, and release acceptance target for the first public macOS build. These decisions are not reopened unless an Apple API, the macOS 13 minimum, or the security model makes one infeasible. A failed prototype must identify the blocked requirement and security differences of alternatives; it must not silently weaken policy.

## Product and release target

- Minimum macOS 13; separate Intel (`amd64`) and Apple Silicon (`arm64`) DMGs. The first release is neither Universal 2 nor a PKG.
- Sign the app and bundled, version-matched privileged helper with Developer ID Application, enable Hardened Runtime, notarize, staple, and verify with Gatekeeper. The first release does not use App Sandbox.
- The GUI always runs as the logged-in user. Only the signed helper performs raw-disk operations.
- Production has no `diskutil`, `plutil`, or `osascript` dependency.
- Uncertain safety evidence always fails closed. There is no force, ignore, allowlist, or continue-anyway path.

The current Linux-only release workflow does not satisfy this target.

## Final boundaries and dependency direction

`internal/disk` is the only platform-neutral disk model, identity policy, destructive-operation contract, error vocabulary, refresh/revalidation policy, and unmount/eject interface.

```text
Fyne GUI -> application/service layer
                    |-> disk.Manager
                    |     -> internal/disk/darwin
                    |        -> internal/disk/darwin/native
                    |           (CoreFoundation, Disk Arbitration, IOKit)
                    `-> privileged operation client
                           -> authenticated XPC
                              -> privileged helper
```

`internal/macos.Backend` remains the production migration implementation until cutover; it is then retired. Phase 0 must not replace it. Choosing `internal/disk` does not permit raw descriptors in the GUI, require a big-bang removal of `device.Device`, or put every Darwin concern in one package.

Native bindings are separated by subsystem:

- `internal/disk/darwin/native`: CF, Disk Arbitration, and IOKit ownership, callbacks, run-loop integration, and framework loading;
- `internal/filepicker/darwin/native`: AppKit and `NSOpenPanel`;
- planned `internal/privilege/darwin/native`: XPC, authorization, and ServiceManagement; the helper is a separate command.

CF/DA/IOKit objects, AppKit pointers, and XPC handles never cross a nested native boundary. Parent packages expose Go scalars, immutable snapshots, typed errors, bounded streams, authenticated message models, or controlled FD/pipe wrappers only.

## Identity and fail-closed policy

The raw BSD path is a **locator**, never identity. It can be reused and is not an authorization token. Operation-lifetime identity uses IOKit `RegistryEntryID`, capacity, whole/removable/external status, system-disk exclusion, permitted transport ancestry, and serial binding when present. Registry path, vendor, product, media name, and mount snapshots are supporting evidence only.

If a serial is observed at selection, it must remain present and equal at every refresh and helper re-enumeration. Serial is not mandatory: a no-serial device may be used only for the current explicit selection and enumeration lifetime. Disappearance or re-enumeration invalidates selection; reinsertion requires selection again even at the same port. Path, size, and model cannot restore it.

A card-reader serial identifies the reader, not removable media. It may support transport evidence but cannot prove the card is unchanged. Media removal invalidates selection. If media-level evidence is unavailable, operation lifetime plus capacity and safety properties bound the risk; there is still no override.

Destructive work is refused if identity, locator, capacity, whole/removable/external/non-system status, permitted ancestry, mount state, refresh, serial binding, disappearance history, helper/client authenticity, protocol version, post-open target identity, unmount, flush, verification, or cancellation state cannot be proven.

## Privileged operation model

The ordinary-user GUI selects and opens the image, detects/decompresses it, shows progress, and streams bounded bytes through an FD/pipe. It never opens or holds a raw-disk descriptor and never asks root to open an arbitrary pathname.

The authenticated, version-matched helper independently re-enumerates, resolves the current locator, reconstructs and compares identity, verifies every safety property, unmounts and refreshes, opens the raw device, writes, flushes, supports read-back verification and formatting, coordinates eject, and reports structured progress, errors, and cancellation completion. It trusts none of the GUI's path, mount, removability, or system-disk assertions.

For macOS 13+, `SMAppService` is preferred. The app and helper are bundled and signed separately. Upgrade, rollback, removal, stale-service behavior, client authentication, and protocol compatibility must be explicit. If a prototype proves `SMAppService` unsuitable, architecture review is required; elevating the GUI is not a fallback.

## Native capability ownership

| Subsystem | Required production capabilities |
|---|---|
| CoreFoundation | Value conversion, retain/release, run loop, callback context |
| Disk Arbitration | Enumeration/events, description, mount state, unmount/eject callbacks, cancellation, refresh |
| IOKit | Registry identity, USB ancestry, vendor/product/serial, safety evidence, reader/media chain |
| Helper | Locator resolution, raw open/write/flush, format, read-back support, privileged lifecycle |
| File picker | AppKit `NSOpenPanel.runModal`, cancellation, Unicode and special-character paths |

The public release may not use `diskutil` for discovery/info/unmount/eject, `plutil` for conversion, or `osascript` for its picker. A modal `NSOpenPanel` without Fyne-window sheet binding is acceptable initially.

## Fixed migration sequence

| Phase | Exit criteria |
|---|---|
| 0: safety baseline | Test the existing backend's revalidation, timeout, cancellation, and error mapping; separate production and PoC documentation; prepare hardware records. **Do not change the production backend.** |
| 1: native identity | Obtain registry ID, vendor/product/serial and USB ancestry; distinguish reader and media; bind selection to an identity snapshot and enumeration lifetime. `diskutil` may supply candidate BSD names only. |
| 2: native discovery | DA enumeration/events plus IOKit evidence produce and refresh `disk.Disk`; no production `diskutil list/info`. Disk number remains a locator. |
| 3: native mount state | Inspect all partitions/mounts, distinguish whole disks and volumes, compare before/after state, and fail closed on uncertainty without command parsing. |
| 4: native lifecycle | DA unmount/eject with completion, bounded timeout, cancellation, callback-token cleanup, balanced scheduling, revalidation, post-operation refresh, and canonical errors. |
| 5: privileged raw operations | Authenticated XPC helper re-enumerates/resolves/opens, accepts an FD/pipe stream, writes/flushes/formats/verifies, supports progress/cancellation, and enforces app/helper version binding. |
| 6: production cutover | Native disk capabilities and helper are complete; GUI has no raw open; both architectures and exact-RC hardware tests pass; rollback is validated. Retire `internal/macos.Backend`. |
| 7: public release | Native picker, two signed/notarized/stapled DMGs, Gatekeeper verification, checksums, helper validation, and exact-RC acceptance. |

Native-path failure never falls back within an operation to weaker path/size/model evidence, stale GUI state, or GUI raw open. Before cutover, a whole build may deliberately use the legacy backend and a development build may select native code. After cutover, rollback is a whole-version rollback; there is no automatic destructive fallback.

## Release Definition of Done

- `internal/disk` is the sole neutral contract; Darwin implements all manager operations and there is no duplicate identity policy.
- Registry ID anchors the selected enumeration lifetime; serial binds when present; reader serial is not media serial; no-serial reinsertion requires a new selection; all uncertainty fails closed with no override.
- Discovery, refresh, mount inspection, unmount/eject, cancellation, timeout, ownership, and post-operation refresh are native and tested on Intel and Apple Silicon. Production uses neither `diskutil` nor `plutil`.
- The authenticated helper owns re-enumeration, locator resolution, raw open, write/flush/format/verification support, progress, and cancellation. The GUI stays unprivileged and streams bytes rather than paths.
- `NSOpenPanel` replaces `osascript` and handles cancellation and Unicode paths.
- Separate `amd64` and `arm64` DMGs meet signing, Hardened Runtime, notarization, stapling, Gatekeeper, bundled-helper, and checksum requirements.
- Exact artifacts pass required physical tests on Intel and Apple Silicon.

## Physical acceptance

The maintainer runs HW-01 through HW-09 on each exact release-candidate artifact and keeps a local PASS/FAIL record containing commit, artifact name and SHA-256, architecture, Mac model, macOS/helper versions, signing identity, notarization result, individual results, overall result, date, maintainer, and notes. A required failure blocks stable release.

Photos, video, a second reviewer, public evidence, uploaded serial numbers, and external approval are explicitly not required.

## Prototype questions that do not reopen product policy

Technical validation remains for PureGo/AppKit main-thread behavior, Fyne activation, XPC/ServiceManagement bindings, `SMAppService` privilege lifecycle, FD transfer, helper upgrades, DA enumeration and multi-partition state, RegistryEntryID behavior, card-reader media evidence, Hardened Runtime entitlements, signing order, and DMG notarization automation. Results must report the single blocked target and alternatives with security impact.
