# macOS 原生 backend Phase 1

[English](MACOS-NATIVE-PHASE1.md)

這是隔離的proof of concept，**不會**切換production macOS backend，也不會刪除現有實作。

具約束力的 production 最終目標與固定 migration 順序見
[`MACOS-CONVERGENCE.zh-TW.md`](MACOS-CONVERGENCE.zh-TW.md)。Phase 0 不改動
production backend，native operation 證據不足時不得 fallback 到較弱證據。

## Public API mapping

| Framework | 驗證API |
|---|---|
| CoreFoundation | `CFRelease`、`CFRetain`、CF string/number/type轉換、dictionary lookup、current run loop及有界run-loop execution |
| Disk Arbitration | `DASessionCreate`、`DADiskCreateFromBSDName`、`DADiskCopyDescription`、disk-appeared callback registration、run-loop schedule/unschedule |
| IOKit | `IOBSDNameMatching`、`IOServiceGetMatchingService`、IORegistry parent traversal、registry ID/path/name/property lookup、`IOObjectRelease` |

所有binding集中於 `internal/disk/darwin/native`；parent package只透過小型adapter暴露一般Go值。

## Ownership規則

- 名稱含 `Create` 或 `Copy` 的function所回傳object，都以 `CFRelease` 精確release一次。
- `CFDictionaryGetValue` 回傳borrowed value，不可release。
- 每個帶owned reference的IOKit service或parent都以 `IOObjectRelease` 釋放；matching dictionary由 `IOServiceGetMatchingService` 消耗。
- Disk Arbitration callback argument只在callback期間borrow。Native code只收到數字token，不收到Go pointer。Callback trampoline為process lifetime；operation unschedule後會刪除token state。

## CI驗證

GitHub Actions有Intel及Apple Silicon專用job，會載入三個system framework、建立並release DA session、描述 `disk0`、轉換真實CF object、traverse IORegistry並等待Disk Arbitration callback。另一項cancellation test檢查callback能有界停止。

Linux開發host可以驗證format、package boundary及一般Go suite，但無法執行Apple framework。只有兩個macOS job都通過後，Phase 1才算完成runtime驗證。在此之前production仍使用既有backend，也不宣稱已有native fallback。
