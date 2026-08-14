# macOS native backend Phase 1

[Traditional Chinese / 繁體中文版](MACOS-NATIVE-PHASE1.zh-TW.md)

This is an isolated proof of concept. It does **not** switch the production
macOS backend and does not remove its existing implementation.

The binding production target and fixed migration sequence are defined in
[`MACOS-CONVERGENCE.md`](MACOS-CONVERGENCE.md). Phase 0 does not change the
production backend, and native uncertainty never falls back to weaker evidence.

## Public API mapping

| Framework | APIs exercised |
|---|---|
| CoreFoundation | `CFRelease`, `CFRetain`, CF string/number/type conversion, dictionary lookup, current run loop and bounded run-loop execution |
| Disk Arbitration | `DASessionCreate`, `DADiskCreateFromBSDName`, `DADiskCopyDescription`, disk-appeared callback registration, run-loop schedule/unschedule |
| IOKit | `IOBSDNameMatching`, `IOServiceGetMatchingService`, IORegistry parent traversal, registry ID/path/name/property lookup, `IOObjectRelease` |

All bindings are in `internal/disk/darwin/native`. The parent package exposes
only ordinary Go values through a small adapter.

## Ownership rules

- Objects returned by functions containing `Create` or `Copy` are released
  exactly once with `CFRelease`.
- Values returned by `CFDictionaryGetValue` are borrowed and are not released.
- Every IOKit service or parent returned with an owned reference is released
  with `IOObjectRelease`; the matching dictionary is consumed by
  `IOServiceGetMatchingService`.
- Disk Arbitration callback arguments are borrowed only during the callback.
  Native code receives a numeric token, never a Go pointer. The callback
  trampoline has process lifetime, while token state is removed after the
  operation is unscheduled.

## CI validation

Dedicated Intel and Apple Silicon GitHub Actions jobs load all three system
frameworks, create/release a DA session, describe `disk0`, convert real CF
objects, traverse IORegistry, and wait for a Disk Arbitration callback. A
separate cancellation test checks bounded callback shutdown.

The Linux development host can validate formatting, package boundaries and the
ordinary Go suite, but cannot execute Apple frameworks. Phase 1 is considered
runtime-validated only after both macOS jobs pass. Until then, production still
uses the existing backend and there is no native fallback claim.
