# GoFlasher 實體硬體測試清單

[English](HARDWARE-TESTING.md)

本清單只說明發布硬體測試需要的實體設備與操作。命令、測試工具參數及正式的
通過／失敗規則，仍以
[`cmd/usbwriter-hwtest/spec-v1.md`](../cmd/usbwriter-hwtest/spec-v1.md) 為準。

> **危險：**測試會清除選定 USB 裝置的全部內容。不可使用系統碟、備份碟，或
> 存有任何資料唯一副本的裝置。

## 所需硬體

每個支援平台都要準備：

- 一台 Linux、Windows 或 macOS 實體電腦；
- 兩支可以完全清除的 USB 隨身碟；
- 兩張容易辨識的實體標籤，例如 `GOFLASHER TEST A` 與
  `GOFLASHER TEST B`；
- 盡可能使用直接連接的 USB 埠；
- 一支手機或相機，用來記錄裝置插入及拔除過程；
- 一位測試員，以及一位協助核對裝置和結果的覆核者。

VM 可以用來測試套件安裝，但不能取代實體電腦的 USB 硬體驗收。

開始前：

1. 備份測試電腦。
2. 拔除外接 SSD、硬碟、記憶卡及備份碟。
3. 清空兩支測試隨身碟，確認其中所有資料都可以消失。
4. 在兩支隨身碟貼上不同的實體標籤。
5. 記錄每支隨身碟的品牌、型號、容量，以及可取得的序號。
6. 由第二位覆核者確認兩支裝置都是可丟棄的測試媒體。

## 測試映像範例

必測範圍是依映像格式區分，不是每個 release 都要測完所有 Linux 發行版。以下每個
必測項目選一個範例即可：

| 必測範圍 | 範例 | 檢查目的 |
|---|---|---|
| 一般 ISO | [Debian netinst ISO](https://www.debian.org/distrib/) | ISO 選取、寫入、read-back verification 與 PC 開機 |
| 一般 IMG | 從 [Raspberry Pi OS 官方頁面](https://www.raspberrypi.com/software/operating-systems/)取得並解壓縮的 `.img` | 完整磁碟 IMG 寫入、驗證與 Raspberry Pi 開機 |
| Gzip 壓縮映像 | 從 [LibreELEC Generic 官方下載頁](https://libreelec.tv/downloads/generic/)下載 x86-64 `.img.gz` 映像 | Gzip 串流、進度、取消、驗證與 PC 開機 |
| XZ 壓縮映像 | 從 [Raspberry Pi OS 官方下載目錄](https://downloads.raspberrypi.com/raspios_arm64/images/)下載 `.img.xz` 映像 | XZ 串流、進度、取消、驗證與 Raspberry Pi 開機 |
| 破壞性安全測試映像 | v1 規格定義的固定 256 MiB raw image | 完整寫入、flush、read-back、checksum 與 corruption detection |
| 中斷測試映像 | v1 規格定義的固定 4 GiB 以上 raw image | 寫入中取消及實體拔除 |

LibreELEC 與 Raspberry Pi OS 都直接下載發行者提供的壓縮映像，不要自行壓縮或重新命名。
測試一個官方 `.img.gz` 和一個官方 `.img.xz`，就足以覆蓋兩種壓縮結尾，不必讓每個
發行版都測兩種格式。請選擇檔名符合上述結尾的最新 Generic x86-64 LibreELEC 映像與
Raspberry Pi OS arm64 映像，並在測試前核對發行者提供的 checksum。本測試刻意不使用 OpenWrt。

如果 Raspberry Pi 下載檔是 ZIP，請先取出其中的 `.img`；ZIP 不是 GoFlasher 目前
宣稱支援的輸入格式。

真實世界相容性測試統一選用 [Debian 官方下載頁](https://www.debian.org/distrib/)
提供的**最新 amd64 netinst ISO**。它是唯一的預設相容性範例；每個 release
candidate 不必再同時測試 Puppy Linux、Rocky Linux、Fedora 與 Arch Linux。
Raspberry Pi OS 則保留作為上方獨立的 IMG 範例。

測試 Debian 映像時要記錄完整檔名、版本、架構、byte size、官方 checksum、下載
日期、GoFlasher 版本、host platform、verification 結果及 boot 結果。交給
GoFlasher 前，必須先使用 Debian 發布的 checksum 驗證下載檔。

## 必做的實體檢查

Linux、Windows、macOS 都必須完成下列檢查，並使用實際準備發布的同一個 release
candidate。

### 1. 偵測、拔除與重新連接

- 將測試隨身碟 A 直接連接到電腦。
- 確認 GoFlasher 顯示正確的標籤、型號、容量、序號和裝置身分。
- 拔除 A，確認它從清單消失。
- 將 A 插回同一個 USB 埠，確認系統辨識出同一個實體裝置。
- 將 A 插入另一個 USB 埠，記錄裝置身分是否改變。

### 2. 不可混淆兩支隨身碟

- 讓 GoFlasher 記錄隨身碟 A，然後拔除 A。
- 插入隨身碟 B。
- 即使作業系統重用了相同 device path 或 disk number，也要確認 GoFlasher 不會把
  B 當成 A。
- 重複交換兩支裝置，直到確實觀察到 path 或 disk number 被重用。

### 3. 已掛載的測試裝置

- 在隨身碟 A 建立或使用一個 partition，並正常掛載。
- 選擇 A 進行測試寫入。
- 確認只有 A 上的 partition 被卸載。
- 確認其他磁碟及 mount 都沒有改變。

### 4. 保護本機系統碟

- 拔除兩支測試隨身碟。
- 確認電腦的 boot disk、system disk，以及使用中的 swap 或 pagefile，都不能在
  GoFlasher 中被選取。
- 不可為了證明這項檢查而嘗試破壞性寫入。

### 5. 寫入時取消

- 開始將大型測試映像寫入隨身碟 A。
- 看到寫入進度後、寫入完成前執行取消。
- 確認 GoFlasher 不會回報寫入成功、驗證成功或安全退出成功。
- A 已含有部分資料，之後仍必須視為可丟棄裝置。

### 6. 寫入時拔除

- 用影片持續拍攝有實體標籤的隨身碟和 GoFlasher 視窗。
- 開始將大型測試映像寫入 A。
- 看到寫入進度後，只拔除 A。
- 確認操作失敗，而且不會回報已完成或已驗證。
- 重新插入 A，確認必須重新偵測並重新選擇裝置。

### 7. 完整寫入與驗證

- 將標準的小型測試映像寫入 A。
- 啟用 read-back verification，以及安全退出或 offline 功能。
- 確認寫入與驗證都成功完成。
- 確認畫面上的 checksum 與核准的測試映像 checksum 相同。

### 8. 偵測損壞資料

- 對 A 執行規格指定的 corruption-detection 案例。
- 確認刻意變更資料後，驗證一定會失敗。
- Checksum 不相符時，絕對不能顯示驗證成功。

### 9. 安全移除

- 完成寫入及驗證後，檢查實體裝置和作業系統狀態。
- 確認 A 上沒有任何 partition 仍處於 mounted 狀態。
- Linux 或 macOS 必須顯示裝置已 eject 或 power off。
- Windows 必須顯示 disk 已 offline。
- 看到預期的安全移除狀態後，才能拔除裝置。

## 必須保留的證據

每個平台都要保存：

- 電腦型號與 OS 版本；
- 兩支測試裝置的型號、容量、hardware revision 與連接方式；
- release candidate 版本和 binary hashes；
- 能看出每次拔除測試使用哪支標記裝置的照片或連續影片；
- 九項檢查的完整 log，包括失敗與重測紀錄；
- 將每項檢查標為 pass 或 fail 的結果表；
- 測試員及第二位覆核者的核准紀錄。

公開版本可以隱藏序號與個人識別資料，但必須在受限的發布儲存空間保存未遮蔽版本。

## 發布判斷

若有任何平台或檢查被跳過、受測 binary 不同、未觀察到 path reuse、證據不完整，
或 removal、verification、eject、system-disk protection 還有疑問，就不可將版本
稱為 stable。未完整驗收的結果只能用於明確標示的 pre-release。
