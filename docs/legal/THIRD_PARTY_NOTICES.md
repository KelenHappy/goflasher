# Third-party notices

[Traditional Chinese / 繁體中文版](THIRD_PARTY_NOTICES.zh-TW.md)

This notice documents third-party components that GoFlasher handles explicitly;
it is not necessarily a complete inventory of every transitive Go dependency.
GoFlasher's GPL-3.0 license does not replace the notices and license terms that
apply to these components. A component applies only to platform builds that
include it.

## github.com/ulikunitz/xz

- Purpose: pure-Go XZ encoder and decoder
- Version: v0.5.16 (from `go.mod`)
- License: BSD 3-Clause
- Upstream: <https://github.com/ulikunitz/xz>

Packages that include xz must include its unmodified upstream `LICENSE` file.
Linux packages use the following path:

```text
/usr/share/doc/goflasher/third-party/github.com_ulikunitz_xz_LICENSE
```

On other platforms whose builds include xz, packagers must include both this
notice and that unmodified license file in the installer, application bundle,
or accompanying documentation.

## github.com/ebitengine/purego

- Purpose: Go bindings to documented macOS system frameworks without CGo
- Version: v0.10.2 (from `go.mod`)
- License: Apache License 2.0
- Upstream: <https://github.com/ebitengine/purego>

GoFlasher currently uses PureGo only in its Darwin/macOS native adapter. PureGo
contains code derived from the Go runtime; those portions are covered by the Go
project's BSD 3-Clause license.

macOS packages containing PureGo must include:

- `THIRD_PARTY_NOTICES.md`
- `THIRD_PARTY_NOTICES.zh-TW.md`
- PureGo's unmodified upstream `LICENSE`
- The Go project's `LICENSE` covering the Go-runtime-derived portions
