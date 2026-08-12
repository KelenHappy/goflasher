# 安全性政策

[English](SECURITY.md)

GoFlasher 會直接寫入區塊裝置，因此安全性或裝置選擇錯誤可能造成無法復原的資料遺失。

## 支援版本

安全性修正提供給最新 GitHub release 與目前預設分支。回報問題前，請先升級較舊版本。

## 回報漏洞

請勿為漏洞建立公開 issue。請在儲存庫的 Security 分頁使用 **Report a vulnerability**，私下提交 security advisory，並附上：

- 受影響版本與主機作業系統；
- 儘可能使用可丟棄映像檔與裝置的重現步驟；
- 預期及實際的裝置路徑、型號、序號與容量；
- 已遮蔽個人路徑及裝置序號的 log；
- 你對影響範圍的評估。

維護者應在收到完整報告後七日內確認。目前沒有漏洞獎勵計畫。請勿使用含重要資料的裝置測試，修正發布前也請勿公開細節。

## 驗證 release

每個封裝 release 都含 `SHA256SUMS`。請從同一個 GitHub release 下載並在安裝前驗證：

```sh
sha256sum --check SHA256SUMS
```

Checksum 可偵測下載損壞或發布後遭替換，但不能取代檢查 GitHub release 與儲存庫歷史。

## 權限邊界

Linux GUI 不應以 root 執行，也不會取得 raw-device descriptor。卸載與關閉電源透過 UDisks2 system D-Bus；寫入、回讀及 flush 則由 root 擁有的 `/usr/libexec/goflasher-helper` 執行，並由封裝的 polkit policy 透過 `pkexec` 啟動。

IPC request 不包含任意檔案路徑，只包含已重新驗證的硬體 identity、可取得時的 serial/WWN、預期 major/minor、精確容量，以及 `write`、`read-back`、`flush` 或受限格式化模式。Helper 在開啟任何東西前，會自行解析 `/sys/dev/block/<major>:<minor>`、比對 sysfs identity及容量、檢查衍生 `/dev` node的 block type與device number、拒絕 mounted、system或swap disk，並自行衍生device-node path。裝置遭替換、安全 metadata缺失、request格式錯誤、未知欄位、不支援模式或授權取消都會 fail closed。

每個 helper process只接受一個request及一個有界操作。它不解析映像檔、不列舉呼叫端指定的路徑、不掛載filesystem，也不是通用root service。Polkit使用不保留授權的 `auth_admin`，因此每個raw write/read-back/flush階段都可能出現驗證提示。Helper與policy的套件ownership是信任邊界的一部分；AppImage使用者必須先把隨附且可稽核的副本安裝到固定、由root擁有的位置。
