# 第三方軟體聲明

[English](THIRD_PARTY_NOTICES.md)

GoFlasher 的編譯binary包含下列第三方軟體。GoFlasher的GPL-3.0授權不會取代這些元件各自適用的聲明與授權條款。

## github.com/ulikunitz/xz

- 用途：純Go XZ stream encoder與decoder
- 版本：請參閱 `go.mod`
- 授權：BSD 3-Clause
- 上游：<https://github.com/ulikunitz/xz>

Binary package必須包含該dependency未修改的上游 `LICENSE`。GoFlasher封裝script會從Go module graph選定的版本複製該檔案至：

```text
/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE
```

Windows與macOS封裝者必須在installer、application bundle或隨附文件中，同時包含本聲明及未修改的license檔案。

## github.com/ebitengine/purego

- 用途：不使用CGo，從Go呼叫documented macOS system framework
- 版本：請參閱 `go.mod`
- 授權：Apache License 2.0
- 上游：<https://github.com/ebitengine/purego>

此dependency會編譯進Darwin native adapter，不需終端使用者安裝runtime。未來macOS package必須隨本聲明附上未修改的上游license。
