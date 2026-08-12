# Third-party notices

[Traditional Chinese / 繁體中文版](THIRD_PARTY_NOTICES.zh-TW.md)

GoFlasher incorporates the following third-party software into its compiled
binaries. GoFlasher's GPL-3.0 license does not replace the notices and license
terms that apply to these components.

## github.com/ulikunitz/xz

- Purpose: pure-Go XZ stream encoder and decoder
- Version: see `go.mod`
- License: BSD 3-Clause
- Upstream: <https://github.com/ulikunitz/xz>

Binary packages must include the dependency's unmodified upstream `LICENSE`
file. GoFlasher's packaging scripts copy that file from the version selected by
the Go module graph to:

```text
/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE
```

Windows and macOS packagers must include both this notice and that unmodified
license file in the installer, application bundle, or accompanying
documentation.

## github.com/ebitengine/purego

- Purpose: Go bindings to documented macOS system frameworks without CGo
- Version: see `go.mod`
- License: Apache License 2.0
- Upstream: <https://github.com/ebitengine/purego>

The dependency is compiled into the Darwin native adapter; it does not require
an end-user runtime installation. Future macOS packages must include the
unmodified upstream license with this notice.
