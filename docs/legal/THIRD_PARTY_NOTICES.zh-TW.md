# 第三方軟體聲明

[English](THIRD_PARTY_NOTICES.md)

本聲明記載目前已明確檢視的第三方元件；final artifact完整compiled Go module inventory
仍必須另行產生及核准，本文件本身不代表已完成clearance。GoFlasher的GPL-3.0不會取代
各元件條款；元件僅適用於包含它的平台artifact。

## github.com/ulikunitz/xz

- 用途：純 Go XZ 編碼器與解碼器
- 版本：v0.5.16（來自 `go.mod`）
- 授權：BSD 3-Clause
- 上游：<https://github.com/ulikunitz/xz>

包含 xz 的版本必須包含其未修改的上游 `LICENSE`。在所有平台上，該文字由
`internal/legal` 內嵌於executable並顯示於「設定」，使用者即由此取得；內嵌文字
由實際編譯進binary的module產生，若某相依套件未提供授權檔案，建置會直接失敗。
其他平台的packager必須以相同方式內嵌，或在application package或隨附文件中包含
該未修改的授權檔案。

## github.com/ebitengine/purego

- 用途：不使用CGo的macOS system framework binding
- 版本：v0.11.1（來自 `go.mod`）
- 授權：Apache License 2.0
- 上游：<https://github.com/ebitengine/purego>

GoFlasher在macOS native adapter使用PureGo。PureGo
包含衍生自Go runtime的程式碼；該部分適用Go專案BSD 3-Clause條款。

包含 PureGo 的版本必須包含：

- `THIRD_PARTY_NOTICES.md`
- `THIRD_PARTY_NOTICES.zh-TW.md`
- PureGo 未修改的上游 `LICENSE`
- 涵蓋衍生自 Go runtime 部分的 Go 專案 `LICENSE`

以上四項在所有平台皆內嵌於executable並顯示於「設定」。套件另會將 GoFlasher 自身
的授權以檔案形式安裝，因為各發行版規範要求如此：Debian 與 RPM 套件置於
`/usr/share/doc/goflasher/copyright`，Arch 套件置於
`/usr/share/licenses/goflasher/LICENSE`，macOS bundle 置於
`Contents/Resources/legal/LICENSE`。

## wimlib / libwim

- 版本：1.14.5，以上游原始檔案未修改建置
- 用途：開啟及分割WIM；透過cgo靜態連結進Linux、macOS與Windows的GUI執行檔
- Library授權：GPL-3.0-or-later或LGPL-2.1-or-later雙授權（因建置排除ntfs-3g，可選
  LGPL）；GoFlasher採用LGPL-2.1-or-later。部分library檔案為MIT授權（Eric Biggers；
  Yuta Mori的libdivsufsort-lite）。
- 上游：<https://wimlib.net>
- Compliance record：`docs/legal/wimlib-1.14.5`（核准的archive hash、build metadata、
  dependency report、授權清單與全文、Corresponding Source與relink說明、核准紀錄）

此分類不會把GoFlasher或libwim連結的作業系統library重新分類。連結wimlib的套件會在
執行檔旁附上compliance record（含wimlib授權全文），其release也會附上wimlib原始碼
archive。允許為debug對libwim的修改而reverse engineering GoFlasher。Release workflow只
連結SHA-256列於record中的archive；未連結wimlib的建置會將Windows安裝媒體builder顯示為
unavailable。

## UEFI component — 禁止進入release

UEFI維持non-MVP；GoFlasher不bundle UEFI implementation、firmware、shim、bootloader或
development package。未針對實際source snapshot/binary確認`GPL-2.0-only`、
`GPL-2.0-or-later`、linking/syscall/firmware/runtime exception、內含component、GPL-3.0
相容性及全部散布義務以前，不得進入release。Gate會拒絕含已知UEFI component名稱的
payload。使用者提供ISO內的檔案是輸入資料，不屬於GoFlasher application package。
