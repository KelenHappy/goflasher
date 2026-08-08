# Third-party notices

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
