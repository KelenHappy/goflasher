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

Go/Fyne/CGo toolchain是source build dependency，不是runtime dependency，也不應bundle進application。Gzip及XZ decoder為純Go並編譯進程式。Windows公開release應code-sign；macOS app bundle應code-sign及notarize。

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

```powershell
go test ./...
go build -trimpath -tags fyne -o dist\goflasher.exe ./cmd/usbwriter
```

以Administrator執行 `dist\goflasher.exe`，讓Windows允許lock/dismount及raw open；backend仍會在destructive access前重新驗證identity。

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

AppImage含helper及policy，但不能從暫時mount安全地安裝它們。請先稽核，再依README安裝到固定system path；完成前raw access會fail closed。

## RPM package

```sh
packaging/make-rpm.sh dist/goflasher 1.0.0 dist
sudo dnf install ./dist/goflasher-1.0.0-1*.x86_64.rpm
```

RPM同樣把helper與polkit policy裝到固定位置，並宣告Fedora/RHEL相容distribution所需runtime dependency。

## 第三方授權聲明

所有binary distribution都必須包含 `THIRD_PARTY_NOTICES.md`、`THIRD_PARTY_NOTICES.zh-TW.md` 及編譯dependency未修改的license檔案。詳細內容見兩份notice。

## Checksum

所有release artifact完成後才產生：

```sh
(cd dist && sha256sum *.deb *.AppImage > SHA256SUMS)
cd dist && sha256sum --check SHA256SUMS
```

## GitHub Actions release

`.github/workflows/release.yml` 在符合 `v*` 的tag或manual dispatch執行：headless tests、Linux Fyne build、Debian/AppImage封裝、產生checksum、上傳artifact；tag build另附加至GitHub release。Manual run不會發布release。

## Windows尚待完成

GUI、Explorer chooser、native discovery、repeated identity check、volume lock/dismount、raw write、read-back、in-process FAT32、flush及safe eject已實作且不依賴PowerShell。公開release仍需hardware-isolated tests、signed packaging及release jobs；未來dedicated privileged helper可改善權限邊界。

## macOS尚待完成

Native discovery、operation-lifetime identity、mount inspection、unmount/eject callback、post-operation refresh 與 AppKit picker 均不使用 `diskutil`、`plutil` 或 `osascript`。Release workflow 會建立分架構 DMG 並執行 signing、notarization、stapling、Gatekeeper 與 checksum；stable 仍由 authenticated XPC helper cutover 與 exact-RC hardware acceptance 阻擋。
