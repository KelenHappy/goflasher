# 測試 GoFlasher

[English](TESTING.md)

## 自動化測試

執行完整headless suite：

```sh
go test ./...
go test -race ./...
go vet ./...
test -z "$(gofmt -l .)"
```

測試使用temporary regular file及假的sysfs/procfs tree，絕不可寫入真實block device。它們只能補充、不能取代三平台實體媒體驗收。

XZ fixture由編譯進程式的純Go實作產生及解碼；CI會拒絕重新加入外部 `xz` runtime dependency：

```sh
go test ./internal/image -run XZ
```

三平台CI也會編譯及測試 `internal/disk`。Linux使用temporary sysfs、mountinfo、`/run/udev/data`與假的UDisks client；Windows透過注入的Win32 fake測試native backend；macOS分別測試compile-safe `disk.Manager` outline與active writer backend。共用FAT32 formatter以temporary disk image測試。Linux unmount與power-off透過純Go `godbus/dbus` client呼叫UDisks2 system D-Bus；CI會拒絕Go code重新呼叫 `udisksctl`或 `udevadm`。

## GUI build檢查

安裝README所列Fyne Linux dependency後執行：

```sh
go test -tags fyne ./cmd/usbwriter
go build -tags fyne ./cmd/usbwriter
```

Linux image chooser應開啟內建Fyne browser而不呼叫desktop portal；檢查英文、繁中、取消、含空白路徑及所有支援suffix。Windows與macOS應開啟原生chooser。

## Package smoke test

在可丟棄VM確認：

1. `sha256sum --check SHA256SUMS` 成功；
2. AppImage在 `chmod +x` 後可啟動；
3. Debian package可用 `apt install ./goflasher_*.deb` 安裝；
4. desktop launcher以一般使用者開啟GoFlasher；
5. 選檔及取消不會錯誤改變application state。

## 有版本的實體硬體驗收

具規範效力的destructive procedure為 [`cmd/usbwriter-hwtest/spec-v1.zh-TW.md`](../../cmd/usbwriter-hwtest/spec-v1.zh-TW.md)。操作員亦應依 [實體硬體測試手冊](HARDWARE-TESTING.zh-TW.md) 逐步操作；spec仍是pass/fail的最高依據。

Harness在Linux、Windows及macOS建置，並強制要求：

- 經review的 `goflasher-hwtest/v1` allowlist，包含精確identity、model、capacity、可取得時的serial及 `disposable: true`；
- 使用明確 `--device-id`，不可用path、disk number或list position；
- 每個destructive case都需新的隨機 `ERASE <identity> <nonce>` confirmation，且觸碰裝置前即消耗；
- 真實可丟棄媒體；VM、loop device、VHD及sparse file不符合硬體驗收。

在核對顯示的identity、實體label、capacity與allowlist前，絕不可貼上one-time confirmation。Harness不會自動選第一個device，real-device test不進CI。

### 核准硬體矩陣

| 平台 | 必要主機及連接 | 必要可丟棄target | 必要權限路徑 |
|---|---|---|---|
| Linux | 實體x86-64或arm64、目前支援distribution、直接USB-A/C | 一支明確識別flash drive及第二支做path reuse；支援時另測讀卡機 | 一般使用者GUI、封裝且root-owned helper與polkit policy |
| Windows | 支援build的實體Windows 11、直接USB-A/C | 兩支具穩定且不同identity/serial的USB drive | 文件所述Administrator/UAC context |
| macOS | 支援的Intel或Apple Silicon實體Mac、直接port或記錄的Apple adapter | 兩支device-tree identity不同的external removable USB drive | 文件所述權限context |

每個RC evidence manifest都要用實際make/model、revision、serial（受限證據）、capacity、connection、host、OS build及backend binary hash取代泛稱。只測hub、只測一顆未觀察到address reuse的target，或只測matrix外單一CPU/OS都不算核准。

### 測試映像檔

| Image ID | 內容與大小 | 用途 | 必要紀錄 |
|---|---|---|---|
| `hwtest-v1-verify-256m.raw` | deterministic、不可壓縮256 MiB；前4 bytes不得為 `00 ff 47 46` | write、flush、read-back、corruption、eject | 精確byte count、SHA-256、generator source/version |
| `hwtest-v1-interrupt-4g.raw` | deterministic、不可壓縮、至少4 GiB；太快時加大 | write期間cancel及remove | 精確byte count、SHA-256、generator source/version |

不要用OS installer當canonical image；mutable metadata與compression會讓timing及corruption evidence難以重現。Image必須能放入所有核准target。

### 必要行為覆蓋

每個平台都執行spec的HW-01至HW-09，涵蓋unplug/reinsert、重新enumerate、Linux/macOS device-node或Windows disk-number reuse、mounted partition、swap/system拒絕、in-flight cancel/remove、flush、read-back checksum、刻意corruption及eject。Harness印出的PASS必須有before/after平台證據支持才有效。

### 每個RC保留的證據

建立以tag及commit命名的read-only directory，例如 `goflasher-v1.2.0-rc1-<commit>/`，包含：

1. RC commit、source archive/GUI/helper/harness/package hash、build log與適用的signing/notarization identity；
2. review後allowlist與硬體manifest；
3. test-image manifest及可重現generator資訊；
4. 每個HW case有timestamp的完整stdout/stderr、失敗及重測，另有三平台最終index；
5. before/after disk inventory、mount/partition/swap、address reuse、system log、權限及eject狀態；
6. 可證明insert/remove、target label、confirmation、cancel、authorization及safe removal對應log的錄影或連續照片；
7. tester與獨立reviewer簽署的HW-01至HW-09結果、deviation、redaction及未解缺陷。

未遮蔽證據至少保存至該stable release支援期限結束。公開證據可遮serial及host/user identity，但必須保留hash、model、capacity、OS version、case outcome及review approval，且不可讓人無法辨識同一case所用實體target。

## Stable release gate

精確RC必須在Linux、Windows及macOS完成並通過完整核准matrix、封存全部證據並取得雙人核准，才可發布stable。驗收後任何binary或backend變更都會使相關平台結果失效。

跳過case、缺證據、未觀察到path/disk-number reuse、system/swap disk出現在allowlist、意外mount、verification/flush不明確或removal/eject問題未解，皆會阻擋release。Prerelease可記錄未完成驗收，但三平台gate全數通過前不得宣稱stable。
