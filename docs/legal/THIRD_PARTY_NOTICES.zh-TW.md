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

包含 xz 的套件必須包含其未修改的上游 `LICENSE` 檔案。Linux 套件使用以下路徑：

```text
/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE
```

Windows portable ZIP會在頂層放置本聲明，並將所有編譯進executable的Go module未修改
授權檔案放在`licenses/`。其他包含xz的平台版本也必須在application package或隨附
文件中包含本聲明與該未修改的授權檔案。

## github.com/ebitengine/purego

- 用途：Linux及macOS bundled libwim dynamic-library bridge，以及不使用CGo的
  macOS system framework binding
- 版本：v0.10.2（來自 `go.mod`）
- 授權：Apache License 2.0
- 上游：<https://github.com/ebitengine/purego>

GoFlasher在Linux與macOS libwim bridge及macOS native adapter使用PureGo。PureGo
包含衍生自Go runtime的程式碼；該部分適用Go專案BSD 3-Clause條款。

包含 PureGo 的 macOS 套件必須包含：

- `THIRD_PARTY_NOTICES.md`
- `THIRD_PARTY_NOTICES.zh-TW.md`
- PureGo 未修改的上游 `LICENSE`
- 涵蓋衍生自 Go runtime 部分的 Go 專案 `LICENSE`

Linux package亦有相同義務，並將文字安裝於
`/usr/share/doc/goflasher/third-party/`。

## wimlib / libwim — 尚未核准發布

- 預定版本：1.14.5
- 預定用途：透過PureGo bridge開啟及分割WIM
- Library授權：LGPL-2.1-or-later
- 相容性：可作為GoFlasher的一部分依GPL-3.0散布，但必須滿足下列artifact-specific條件

LGPL-2.1-or-later分類適用於wimlib 1.14.5 library，不會把GoFlasher、optional component
或native transitive dependency重新分類。發布前必須以實際bundled artifact核對精確source
snapshot、全部適用source header與license/notice、build configuration、啟用的optional
feature，以及每個實際link或bundle的native transitive dependency。Release record亦須保存
source/binary SHA-256、toolchain/linker版本、flags、patch、dependency report、license text、
legal approval，以及已驗證可取得且包含build/install與適用relink材料的Corresponding
Source。Package必須保留LGPL notice與license text、允許替換shared library，且不得禁止為
debug library修改而進行reverse engineering。在完成artifact-specific驗證前禁止散布libwim，
Windows builder必須顯示unavailable。

## UEFI component — 禁止進入release

UEFI維持non-MVP；GoFlasher不bundle UEFI implementation、firmware、shim、bootloader或
development package。未針對實際source snapshot/binary確認`GPL-2.0-only`、
`GPL-2.0-or-later`、linking/syscall/firmware/runtime exception、內含component、GPL-3.0
相容性及全部散布義務以前，不得進入release。Gate會拒絕含已知UEFI component名稱的
payload。使用者提供ISO內的檔案是輸入資料，不屬於GoFlasher application package。
