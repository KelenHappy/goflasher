<div align="center">
  <img src="../../packaging/org.goflasher.usbwriter.svg" width="128" height="128" alt="GoFlasher 標誌">

# GoFlasher：以安全為優先的 USB 映像檔寫入工具

**GoFlasher 可在 Linux、Windows 與 macOS 將未壓縮或壓縮的磁碟映像檔寫入
可移除式 USB 隨身碟。**

[English README](../../README.md)
</div>

> [!WARNING]
> 寫入映像檔會清除所選裝置上的所有資料。確認前，請務必核對裝置的型號、
> 序號、容量與裝置路徑。

## 功能

- 寫入 `.iso`、`.img` 與 `.raw` 磁碟映像檔。
- 使用內建 Go 解碼器串流寫入 gzip（`.gz`）與 XZ（`.xz`）映像檔，不需外部解壓縮
  程式，也不必先建立未壓縮的暫存檔。
- 檢查及寫入映像檔時，計算來源檔案的 SHA-256。
- 可選擇回讀已寫入的位元組，並比對 SHA-256 總和檢查碼。
- 顯示寫入與驗證進度、傳輸速度及預估剩餘時間。
- 可取消進行中的操作。
- 寫入成功後，可選擇關閉 USB 裝置電源。
- Linux 使用內建且已本地化的 Fyne 檔案選擇器；Windows 與 macOS 使用系統原生
  檔案選擇器。
- 介面支援英文與繁體中文。
- 在應用程式內保留可複製且有行數上限的活動紀錄。
- 僅允許選取可移除式 USB 隨身碟或讀卡機；在 Linux 上，若 udev 未提供隨身碟
  分類，亦允許容量不超過 128 GB 的一般 `usb-storage` 媒體。
- 拒絕已掛載的重要系統磁碟、交換空間裝置、ATA 裝置、SSD/HDD 型號、
  儲存裝置橋接器、UAS 裝置及容量較大且無法明確辨識的 USB 儲存裝置。
- 在卸載前及開啟原始裝置前，重新驗證裝置身分。
- 提供跨平台且不洩漏原生 handle 或常數的 `disk.Manager` 統一介面，上層只需呼叫
  `disk.NewManager()`。Linux 的 sysfs/udisks 與 Windows 原生實作已啟用；macOS
  目前保留可編譯的雛形，日後再接上 Disk Arbitration/IOKit。

GoFlasher 不會分析、辨識或限制映像檔內含的作業系統。因此，在每個支援的主機
平台上，都可以燒錄 Linux ISO 或其他支援格式的 raw disk image；目前接受
`.iso`、`.img`、`.raw`，以及以 gzip（`.gz`）或 XZ（`.xz`）壓縮的映像檔。

GoFlasher 主要是映像檔寫入工具，也可以清除已選取且受支援的 USB 裝置並建立
名為 `GOFLASHER` 的全裝置 FAT32 檔案系統。Linux、Windows 與 macOS 都使用同一個
內建純 Go 格式化器，不會啟動作業系統格式化工具。它不會下載作業系統
映像檔、建立 Windows 安裝替代方案、建立持久化分割區或執行壞軌測試。

## 主機平台支援

「跨平台」是指 GoFlasher 程式與原始裝置後端可以在 **Linux、Windows 與
macOS 主機**上執行，而不是指映像檔受限於主機平台。在上述三種平台中的任何
一種主機上，GoFlasher 都能將 Linux ISO 或其他支援的 raw disk image 寫入核准的
可移除式媒體。Windows 使用原生 Explorer 檔案選擇器，以及 Win32 SetupAPI、
Configuration Manager、volume-control 與 storage IOCTL；macOS 使用原生 Finder
選擇器與 `diskutil` 執行探索、卸載及退出。兩者的原始磁碟存取都需要
提升權限。

目前的後端會讀取 Linux 的 sysfs、procfs 與 udev 資訊，並透過 system D-Bus 直接
呼叫 UDisks2 執行卸載及關閉裝置電源，不需要 `udisksctl` CLI。請勿以 root 身分執行
GUI；寫入、回讀與 flush 會透過
polkit 呼叫由 root 擁有的專用權限提升輔助程式。

這不表示所有平台都已完全不依賴主機指令。Linux 直接讀取 `/run/udev/data` 補充裝置
分類，只以 `pkexec` 啟動權限提升輔助程式。Windows 的磁碟探索、身分驗證、raw I/O、
volume lock/dismount、flush、格式化及退出都使用原生 API，不啟動 CLI；macOS 仍使用
`diskutil` 管理裝置及 `osascript` 開啟原生選擇器，但格式化在程式內完成。三個平台的原生版本都會在 CI 中建置及測試，
但 Windows/macOS 仍需要原生主機權限及實體媒體驗收才能發布。完整邊界及改用原生 API
的方向請參閱 [BUILDING.md](../development/BUILDING.md#native-api-and-command-dependencies)。

## 預先建置的下載套件

程式碼支援的平台與目前提供預先建置套件的平台是兩件不同的事。Release workflow
會發布Linux packages與Windows amd64 portable ZIP；Windows執行檔目前未經
Authenticode簽章，請以隨附的`.sha256`檔驗證下載內容。
macOS在signed/notarized release gate啟用前仍提供source build。

封裝版本發布後，儲存庫的發行工作流程會在 GitHub Release 提供下列檔案：

- `goflasher_<version>_amd64.deb`
- `goflasher-<version>-1*.x86_64.rpm`
- `goflasher-<version>-1-x86_64.pkg.tar.zst`
- `SHA256SUMS`
- `GoFlasher-<version>-windows-amd64.zip`
- `GoFlasher-<version>-windows-amd64.zip.sha256`

**Windows distribution is portable-only.** 下載ZIP、解壓縮，然後以Administrator
執行`GoFlasher.exe`。GoFlasher不會自動更新；日後請從GitHub Releases手動下載新版。

套件並不是可跨所有平台共用的單一執行檔；每個作業系統及 CPU 架構都必須分別
建置與封裝。Debian 套件透過 APT、RPM 套件透過 DNF 安裝宣告的執行期相依套件。
Windows 目前必須以系統管理員身分執行；macOS 目前仍需提升 raw disk 存取權限，
而公開發行也分別需要 code signing，以及 macOS notarization。gzip 與 XZ 解碼器
都會編譯進 GoFlasher，因此各平台套件皆不需額外安裝解壓縮程式。

執行或安裝從發行頁面下載的檔案前，請先驗證檔案：

```sh
sha256sum --check SHA256SUMS
```

Debian、RPM 與 Arch Linux 套件會把 privileged helper 與具名的 GoFlasher polkit
action 安裝到固定且 root 擁有的系統路徑，不需要另外安裝 helper。

Linux 一律使用已包入 GoFlasher 的 Fyne 映像檔選擇器；選檔流程不會呼叫 XDG
Desktop Portal、D-Bus、`kdialog`、Zenity、Dolphin 或 Nautilus，因此不需要任何
桌面環境專用套件。選擇器標題、「選擇」與「取消」按鈕會跟隨
GoFlasher 的英文或繁體中文介面語言；檔案、資料夾、常用位置、新增資料夾及
顯示隱藏檔案等 Fyne 選擇器介面文字也提供繁體中文翻譯。實際目錄名稱維持
檔案系統中的名稱，不會翻譯或改名。

在 Debian 或 Ubuntu 安裝 Debian 套件：

```sh
sudo apt install ./goflasher_*_amd64.deb
```

在 Fedora、RHEL 或相容發行版安裝 RPM 套件：

```sh
sudo dnf install ./goflasher-*.x86_64.rpm
```

請勿使用 `sudo` 啟動 GoFlasher 本身。

## 建置

GoFlasher 需要 [`go.mod`](../../go.mod) 指定的 Go 版本。建置 Fyne GUI 還需要 Linux
的 OpenGL、X11 與 Wayland 開發套件。相依套件安裝、從原始碼建置、
Debian/RPM 封裝方式及目前的 Windows/macOS 限制，請參閱 **[BUILDING.md](../development/BUILDING.md)**。

開發用 GUI 可使用下列指令啟動：

```sh
go run -tags fyne ./cmd/usbwriter
```

若未指定 `fyne` 標籤，`cmd/usbwriter` 只會執行不含相依套件、供無圖形介面
環境使用的資訊啟動程式。

在 Windows 的系統管理員 Command Prompt 中，可建置完整 GUI：

```bat
go test ./...
go build -trimpath -tags fyne -o dist\goflasher.exe ./cmd/usbwriter
```

請以系統管理員身分執行 `dist\goflasher.exe`，讓程式可以將已確認的可移除式磁碟
設為離線並執行原始寫入。程式仍會在破壞性存取前重新驗證磁碟身分。

在 macOS 建置 GUI：

```sh
go test ./...
go build -trimpath -tags fyne -o dist/goflasher ./cmd/usbwriter
```

在專用權限提升輔助程式完成前，需使用 `sudo` 啟動本機建置的程式，才能存取
`/dev/rdiskN`。GoFlasher 只會顯示被明確認定為外接可移除式 USB 媒體的實體磁碟，
並在存取前重新驗證其實體裝置樹身分。

## 語言

GoFlasher 會依照程序的地區設定選擇語言。可使用 `GOFLASHER_LANG` 覆寫單次
啟動的語言：

```sh
GOFLASHER_LANG=zh-TW go run -tags fyne ./cmd/usbwriter
GOFLASHER_LANG=en go run -tags fyne ./cmd/usbwriter
```

不支援的地區設定會回退為英文。

## 測試

自動化測試只使用暫存的一般檔案及模擬的 sysfs/procfs 目錄樹，不會寫入真正的
區塊裝置。

```sh
go test ./...
```

競態檢查、GUI 檢查、套件煙霧測試，以及刻意加入防護措施的實體裝置測試指令，
請參閱 **[TESTING.md](../development/TESTING.md)**。執行發布驗收時，可依照
[繁體中文實體硬體測試手冊](../development/HARDWARE-TESTING.zh-TW.md)操作，或參考
[英文版](../development/HARDWARE-TESTING.md)。

## 安全性

回報安全漏洞前，請先閱讀 **[SECURITY.md](../../SECURITY.md)**。請勿在公開 Issue
揭露裝置選取或原始寫入相關的安全漏洞。

GoFlasher 是依 **GNU General Public License version 3** 授權的自由軟體。
詳情請參閱 [LICENSE](../../LICENSE)。編譯後的發行版本亦包含 BSD 授權的第三方元件；
其著作權聲明與再散布資訊請參閱
[THIRD_PARTY_NOTICES.md](../legal/THIRD_PARTY_NOTICES.md)。

## Release boundary

Windows GUI、原生 Explorer 選擇器、可移除式裝置探索、重複身分驗證、離線操作、
原始寫入與回讀驗證均已實作。Release workflow會產生amd64 portable ZIP與
checksum；Authenticode簽章尚未啟用，exact artifact仍須完成實體硬體驗收。
Windows永久採單一elevated GUI process，不使用helper、service或跨程序IPC。
Linux仍維持polkit與受限helper架構。

macOS GUI、原生 Finder 選擇器、可移除式 USB 後端、原始寫入、回讀驗證與
`diskutil` 退出操作均已實作。正式發布前仍需要實體硬體隔離測試、專用權限提升
輔助程式、簽署與 notarization，以及發行工作流程。

## 改善建議與錯誤回報

非敏感的錯誤報告與功能建議，請使用
[GitHub Issue 追蹤器](https://github.com/goflasher/goflasher/issues)。回報時請附上
GoFlasher 版本、Linux 發行版、桌面環境，以及經過適當遮蔽的相關紀錄。
