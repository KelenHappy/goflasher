# macOS production 最終收斂目標

[English](MACOS-CONVERGENCE.md)

本文是第一個公開 macOS 版本的架構、migration 與發行驗收依據。除非 Apple API、macOS 13 最低版本或安全模型證明某項不可行，不再重開這些產品決策。Prototype 失敗時必須指出受阻目標及替代方案的安全差異，不得默默降低政策。

## 產品與發行

- 最低 macOS 13；分開發布 Intel (`amd64`) 與 Apple Silicon (`arm64`) DMG；首版不做 Universal 2、PKG 或 App Sandbox。
- App 與內附且版本綁定的 privileged helper 使用 Developer ID Application 簽章，啟用 Hardened Runtime，完成 notarization、stapling 與 Gatekeeper 驗證。
- GUI 永遠以登入使用者權限執行，只有已簽署 helper 可做 raw-disk operation。
- Production 不依賴 `diskutil`、`plutil` 或 `osascript`。
- 安全證據不確定一律 fail closed，沒有 force、ignore、allowlist 或 continue-anyway。

現有只有 Linux job 的 release workflow 尚未達成此目標。

## 最終 boundary 與依賴方向

`internal/disk` 是唯一平台中立 disk model、identity policy、destructive-operation contract、錯誤語意、refresh/revalidation 規則與 unmount/eject 介面。

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

`internal/macos.Backend` 在 production cutover 前仍是 migration implementation，之後 retire；Phase 0 不得替換它。選擇 `internal/disk` 不代表 GUI 可持有 raw descriptor，也不要求 big-bang 移除 `device.Device`。

Native binding 依 subsystem 分離：`internal/disk/darwin/native` 負責 CF/DA/IOKit；`internal/filepicker/darwin/native` 負責 AppKit/`NSOpenPanel`；未來 `internal/privilege/darwin/native` 負責 XPC、Authorization 與 ServiceManagement，helper 為獨立 command。Native object/handle 不得離開 nested package；上層只取得 Go scalar、immutable snapshot、typed error、bounded stream、authenticated message 或受控 FD/pipe wrapper。

## Identity 與 fail-closed 政策

Raw BSD path 只是一個可被 OS 重用的 **locator**，不是 identity 或授權 token。Operation-lifetime identity 使用 IOKit `RegistryEntryID`、capacity、whole/removable/external、system-disk 排除、允許的 transport ancestry，以及存在時的 serial binding。Registry path、vendor、product、media name 與 mount snapshot 只作輔助證據。

選擇時若取得 serial，每次 refresh 與 helper 重新枚舉時都必須仍存在且相同。Serial 不是必要條件；無 serial 裝置只適用本次明確選擇與 enumeration lifetime。裝置消失或重新枚舉立即使 selection 失效，重插同一 port 也必須重選；path、size、model 不能恢復 selection。

Card-reader serial 只識別 reader，不是 media serial。它可支援 transport evidence，但不能證明 card 未更換。Media 移除會使 selection 失效；即使缺少 media-level evidence，也只可用 operation lifetime、capacity 與安全屬性限制風險，仍無 override。

Identity、locator、capacity、whole/removable/external/non-system、transport ancestry、mount state、refresh、serial binding、裝置消失歷史、helper/client authenticity、protocol version、raw open 後 target identity、unmount、flush、verify 或 cancellation state 任一無法證明，必須拒絕 destructive operation。

## Privileged operation 模型

普通權限 GUI 選擇並開啟 image、detect/decompress、顯示進度，再以 bounded FD/pipe 串流 bytes。GUI 不開啟或持有 raw descriptor，也不要求 root 開啟任意 pathname。

Authenticated、版本相容的 helper 自行重新枚舉、resolve locator、重建並比對 identity、驗證安全屬性、unmount/refresh、raw open、write、flush、read-back/verify support、format、eject，並回報 structured progress/error/cancellation。Helper 不信任 GUI 的 path、mount、removable 或 system-disk 宣稱。

macOS 13+ 優先採用 `SMAppService`。App/helper 內附且各自簽署；upgrade、rollback、remove、stale service、client authentication 與 protocol compatibility 都要有明確行為。若 prototype 證明不適用，必須回到架構層決策；提升整個 GUI 不是 fallback。

## 固定 migration 順序

| Phase | Exit criteria |
|---|---|
| 0：安全基線 | 測試現有 backend 的 revalidation、timeout、cancellation、error mapping；區分 production/PoC 文件；準備硬體紀錄。**不改 production backend。** |
| 1：native identity | 取得 registry ID、vendor/product/serial、USB ancestry；區分 reader/media；selection 綁定 identity snapshot 與 enumeration lifetime。`diskutil` 只可供應候選 BSD name。 |
| 2：native discovery | DA enumeration/event 加 IOKit evidence 產生與 refresh `disk.Disk`；production 不再使用 `diskutil list/info`；disk number 只作 locator。 |
| 3：native mount state | 檢查全部 partition/mount，區分 whole disk/volume，比對操作前後狀態；不確定 fail closed，不解析 command output。 |
| 4：native lifecycle | DA unmount/eject completion、bounded timeout、cancellation、callback token cleanup、schedule balance、revalidation、post-refresh 與 canonical error。 |
| 5：privileged raw operations | Authenticated XPC helper 自行 enumerate/resolve/open，接收 FD/pipe stream，write/flush/format/verify，支援 progress/cancellation 與版本綁定。 |
| 6：production cutover | Native disk 能力與 helper 完成；GUI 無 raw open；兩架構與 exact-RC hardware tests 通過；rollback 已驗證；retire `internal/macos.Backend`。 |
| 7：公開發行 | Native picker、兩份 signed/notarized/stapled DMG、Gatekeeper、checksum、helper validation 與 exact-RC acceptance。 |

Native path 失敗時，同一 operation 不得 fallback 到 path/size/model、stale GUI state 或 GUI raw open。Cutover 前，整個 build 可明確使用 legacy backend，development build 可明確選 native code；cutover 後只能整版 rollback，不做 destructive automatic fallback。

## 第一版 Definition of Done

- `internal/disk` 是唯一中立 contract；Darwin 完整實作 Manager，沒有重複 identity policy。
- Registry ID 綁定 enumeration lifetime；有 serial 必須 binding；reader serial 不冒充 media serial；無 serial 重插必須重選；所有不確定皆 fail closed，沒有 override。
- Discovery、refresh、mount、unmount/eject、cancellation、timeout、ownership 與 post-refresh 全部 native，通過 Intel/Apple Silicon 測試；production 不用 `diskutil`/`plutil`。
- Authenticated helper 擁有 re-enumeration、locator resolution、raw open、write/flush/format/verify support、progress/cancellation；GUI 不提升，只串流 bytes，不傳任意 pathname。
- `NSOpenPanel.runModal` 取代 `osascript`，正確處理 cancellation 與 Unicode/特殊字元 path。
- 分開的 `amd64`/`arm64` DMG 完成 signing、Hardened Runtime、notarization、stapling、Gatekeeper、bundled signed helper 與 checksum。
- Exact artifacts 在 Intel 與 Apple Silicon 實體 Mac 通過必要測試。

## 硬體驗收

Maintainer 對每個 exact RC artifact 執行 HW-01～HW-09，並在本地保存 commit、artifact filename/SHA-256、architecture、Mac model、macOS/helper version、signing identity、notarization result、各項與 overall PASS/FAIL、date、maintainer 與 notes。任一必要測試失敗會阻擋 stable release。

不要求照片、影片、第二 reviewer、公開 evidence、上傳硬體序號或外部簽核。

## 不重開產品政策的技術驗證

仍需驗證 PureGo/AppKit main-thread、Fyne activation、XPC/ServiceManagement binding、`SMAppService` privilege lifecycle、FD transfer、helper upgrade、DA enumeration/multiple partitions、RegistryEntryID 行為、card-reader media evidence、Hardened Runtime entitlements、signing order 與 DMG notarization automation。結果必須指出單一受阻目標與替代方案的安全影響。
