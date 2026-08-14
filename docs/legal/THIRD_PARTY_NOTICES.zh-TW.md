# 第三方軟體聲明

[English](THIRD_PARTY_NOTICES.md)

本聲明記載 GoFlasher 明確處理的第三方元件；不一定完整列出所有間接 Go 相依套件。GoFlasher 的 GPL-3.0 授權不會取代這些元件各自適用的聲明與授權條款。元件僅適用於包含該元件的平台版本。

## github.com/ulikunitz/xz

- 用途：純 Go XZ 編碼器與解碼器
- 版本：v0.5.16（來自 `go.mod`）
- 授權：BSD 3-Clause
- 上游：<https://github.com/ulikunitz/xz>

包含 xz 的套件必須包含其未修改的上游 `LICENSE` 檔案。Linux 套件使用以下路徑：

```text
/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE
```

Windows portable ZIP會在頂層放置本聲明，並將所有編譯進executable的Go module未修改
授權檔案放在`licenses/`。其他包含xz的平台版本也必須在application package或隨附
文件中包含本聲明與該未修改的授權檔案。

## github.com/ebitengine/purego

- 用途：不使用 CGo，為有文件說明的 macOS 系統框架提供 Go 綁定
- 版本：v0.10.2（來自 `go.mod`）
- 授權：Apache License 2.0
- 上游：<https://github.com/ebitengine/purego>

GoFlasher 目前僅在 Darwin/macOS 原生介面卡中使用 PureGo。PureGo 包含衍生自 Go runtime 的程式碼；這些部分受 Go 專案的 BSD 3-Clause 授權條款規範。

包含 PureGo 的 macOS 套件必須包含：

- `THIRD_PARTY_NOTICES.md`
- `THIRD_PARTY_NOTICES.zh-TW.md`
- PureGo 未修改的上游 `LICENSE`
- 涵蓋衍生自 Go runtime 部分的 Go 專案 `LICENSE`
