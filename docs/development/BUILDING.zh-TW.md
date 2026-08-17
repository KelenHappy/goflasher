# 建置 GoFlasher

[English](BUILDING.md)

隔離的macOS native proof of concept及CI驗證邊界記錄於
[docs/MACOS-NATIVE-PHASE1.zh-TW.md](../architecture/MACOS-NATIVE-PHASE1.zh-TW.md)。

## 支援的建置目標

Raw-device backend與Fyne GUI可在Linux、Windows及macOS建置。Windows使用原生Win32 SetupAPI、Configuration Manager、volume control與storage IOCTL；raw access需以Administrator執行。macOS 使用 Disk Arbitration 與 IOKit 進行 discovery、identity、mount inspection、unmount/eject，並以 AppKit 選擇 image。

可重用的 `internal/disk` abstraction與writer backend分離；共用 `Manager` API不含native handle或平台constant，`disk.NewManager()`由build tag選擇。Linux使用sysfs、mountinfo、swap inspection及udisks2；Windows使用原生Win32 backend。macOS native manager完成後也必須維持相同介面。

## 需求

- `go.mod` 宣告的Go toolchain版本（目前為Go 1.26.4）。
- Fyne需要的C compiler及Linux development libraries（僅為GUI build requirement）。
- Linux runtime需UDisks2 system service負責unmount與power-off；不呼叫 `udisksctl`。
- Linux受限raw-device操作需polkit/`pkexec`。
- Linux image chooser內建於Fyne，不需portal、D-Bus file chooser、`kdialog`或Zenity。

Ubuntu 24.04：

```sh
sudo apt update
sudo apt install gcc libgl1-mesa-dev xorg-dev libxkbcommon-dev libwayland-dev udisks2
```

## Native API與command dependency

| 主機 | 裝置管理 | 剩餘command | 狀態 |
|---|---|---|---|
| Linux | sysfs、procfs、`/run/udev/data`、UDisks2 system D-Bus | `pkexec`啟動受限helper | GUI及backend已啟用 |
| Windows | SetupAPI、cfgmgr32、`DeviceIoControl`、volume FSCTL、raw `\\.\PhysicalDriveN` | disk operation無CLI | 需Administrator |
| macOS | Disk Arbitration、IOKit、AppKit `NSOpenPanel` 與 migration raw writer | discovery/lifecycle/picker 無 command dependency | privileged raw-operation cutover 仍受 gate 限制 |

Go/Fyne/CGo toolchain是source build dependency，不是runtime dependency，也不應bundle進application。Gzip及XZ decoder為純Go並編譯進程式。Windows公開release executable必須完成Authenticode簽章；macOS app bundle必須code-sign及notarize。

Windows 明確採用單一 elevated GUI process：正式 executable 內含 UAC
`requireAdministrator` manifest，discovery、locking、raw I/O、格式化及退出都在同一
process 執行。Windows 不設 privileged helper、service 或跨程序 IPC，backend 也不會
執行 PowerShell 或其他 command interpreter。這是平台特定的權限模型，不會改動 Linux
既有的 polkit 與受限 helper 架構。

三平台CI只能證明source可編譯及隔離測試通過，不能證明真實硬體上的destructive access；release仍必須通過 `TESTING.zh-TW.md` 的實體媒體驗收。

## 從原始碼建置

先執行：

```sh
go test ./...
```

Linux GUI與helper：

```sh
go build -trimpath -tags fyne -o dist/goflasher ./cmd/usbwriter
go build -trimpath -o dist/goflasher-helper ./cmd/goflasher-helper
sudo install -m 0755 dist/goflasher-helper /usr/libexec/goflasher-helper
sudo install -m 0644 packaging/org.goflasher.usbwriter.policy \
  /usr/share/polkit-1/actions/org.goflasher.usbwriter.policy
```

開發時可執行 `go run -tags fyne ./cmd/usbwriter`；不要以root建置或執行Linux GUI。

Windows：

```bat
go test ./...
go build -trimpath -tags fyne -o dist\goflasher.exe ./cmd/usbwriter
```

以Administrator執行 `dist\goflasher.exe`，讓Windows允許lock/dismount及raw open；backend仍會在destructive access前重新驗證identity。

## Windows portable distribution

**Windows distribution is portable-only.** Windows v1只支援amd64，並在GitHub
`windows-latest` runner建置。永久使用流程為：

```text
Download → Extract ZIP → Run GoFlasher.exe as Administrator
```

Windows不提供installer、service、background updater、registry setup、shell
extension、driver package或automatic update。使用者日後從官方GitHub Releases頁面
手動下載新版。Portable asset及checksum名稱為：

```text
GoFlasher-${VERSION}-windows-amd64.zip
GoFlasher-${VERSION}-windows-amd64.zip.sha256
```

Production ZIP包含已簽章的`GoFlasher.exe`、`README-Windows.txt`、英文與繁中第三方聲明，以及
存放編譯module未修改license的`licenses/`。ZIP不包含簽章certificate、簽章暫存檔、
build cache、source tree或debug symbol。

`packaging/windows/make-portable.go`的跨平台Go command從已建置的executable建立
portable layout與checksum。Release workflow嵌入UAC `requireAdministrator` manifest，以SHA-256及
RFC 3161 timestamp完成Authenticode簽章、驗證簽章，之後才建立ZIP。只有當GitHub Actions
repository variable `WINDOWS_PRODUCTION_READY`明確設為`true`時才會簽章；否則development
與alpha workflow會封裝未簽章的executable。啟用此variable前，maintainer需設定：

- `WINDOWS_CERTIFICATE_PFX_BASE64`：公開code-signing PFX的base64內容；
- `WINDOWS_CERTIFICATE_PASSWORD`：PFX密碼。

PFX只會存在runner temporary directory，並由shell `trap`在所有exit path刪除；不會
複製進portable directory或上傳。

### PowerShell usage audit

Application、Windows backend、portable packager、CI package validation及release/signing
workflow都不執行PowerShell。Portable packager是Go command；workflow使用Git Bash與
Windows SDK tools；source build範例使用Command Prompt；使用者以`certutil`驗證checksum。

目前只在hardware-test spec保留PowerShell command範例，供maintainer選擇性使用
`Get-Disk`、`Get-Partition`及`Get-Volume`蒐集Windows inventory證據。GoFlasher、package、
CI與release workflow都不會執行它們。改用資訊較不完整且已deprecated的inventory
command只會降低驗收證據品質，並不會消除product dependency，因為產品本來就沒有此
dependency。

macOS：

```sh
go test ./...
go build -trimpath -tags fyne -o dist/goflasher ./cmd/usbwriter
```

Darwin manager 透過 PureGo 載入 Disk Arbitration 與 IOKit；picker 使用 AppKit `NSOpenPanel`，兩者都沒有 command fallback。GUI 不得以 `sudo` 執行；authenticated helper/XPC cutover 完成前，raw-operation development 必須 fail closed。

## Debian package

需要 `dpkg-deb`：

```sh
packaging/make-deb.sh dist/goflasher 1.0.0 dist
sudo apt install ./dist/goflasher_1.0.0_amd64.deb
```

套件會安裝helper與polkit action。FAT32由受限helper內的共用formatter完成，不需要dosfstools，也不以root執行filesystem utility。GUI不可設為setuid，也不可用sudo執行。

## AppImage

需要可執行的 `linuxdeploy` 與 `appimagetool`：

```sh
packaging/make-appimage.sh dist/goflasher 1.0.0 dist \
  /path/to/linuxdeploy /path/to/appimagetool
```

AppImage在`usr/libexec`內含可執行helper，並透過`APPDIR`尋找，因此不需要先安裝system helper即可使用。若要使用具名polkit action，可先稽核，再依README安裝到固定system path。

## RPM package

```sh
packaging/make-rpm.sh dist/goflasher 1.0.0 dist
sudo dnf install ./dist/goflasher-1.0.0-1*.x86_64.rpm
```

RPM同樣把helper與polkit policy裝到固定位置，並宣告Fedora/RHEL相容distribution所需runtime dependency。

## Arch Linux package

Release workflow也產生x86-64 native pacman package：

```sh
packaging/make-arch.sh dist/goflasher 1.0.0 dist
sudo pacman -U ./dist/goflasher-1.0.0-1-x86_64.pkg.tar.zst
```

內容包含與Debian/RPM相同的GUI、root-owned helper、polkit policy、desktop metadata、
notice及dependency license。加入Arch package不會改變Linux privilege boundary。

## 第三方授權聲明

所有binary distribution都必須包含 `THIRD_PARTY_NOTICES.md`、`THIRD_PARTY_NOTICES.zh-TW.md` 及編譯dependency未修改的license檔案。詳細內容見兩份notice。

## Checksum

所有release artifact完成後才產生：

```sh
(cd dist && sha256sum *.deb *.rpm *.pkg.tar.zst *.AppImage > SHA256SUMS)
cd dist && sha256sum --check SHA256SUMS
```

## GitHub Actions release

`.github/workflows/release.yml`在符合`v*`的tag或manual dispatch執行：headless tests、Linux package與已Authenticode簽章的Windows amd64 portable ZIP、產生各平台checksum並上傳Actions artifact；`v*` tag build會把平台assets附加到同一個GitHub Release。Manual run只產生Actions artifact，不發布Release。

## Windows release boundary

GUI、Explorer chooser、native discovery、repeated identity check、volume lock/dismount、raw write、read-back、in-process FAT32、flush及safe eject已實作且不依賴PowerShell。Release workflow會產生已簽章的amd64 portable ZIP與checksum；exact release candidate仍必須通過hardware-isolated acceptance。Windows支援的權限架構是上述單一elevated GUI process，不使用helper或service。

## macOS尚待完成

Native discovery、operation-lifetime identity、mount inspection、unmount/eject callback、post-operation refresh 與 AppKit picker 均不使用 `diskutil`、`plutil` 或 `osascript`。Release workflow 會建立分架構 DMG 並執行 signing、notarization、stapling、Gatekeeper 與 checksum；stable 仍由 authenticated XPC helper cutover 與 exact-RC hardware acceptance 阻擋。
