# Third-party notices

[Traditional Chinese / 繁體中文版](THIRD_PARTY_NOTICES.zh-TW.md)

This notice documents components reviewed explicitly so far. The complete
compiled Go-module inventory for a final artifact is still release metadata and
must be generated and approved; this file alone is not evidence of clearance.
GoFlasher's GPL-3.0 license does not replace the notices and license terms that
apply to these components. A component applies only to platform builds that
include it.

## github.com/ulikunitz/xz

- Purpose: pure-Go XZ encoder and decoder
- Version: v0.5.16 (from `go.mod`)
- License: BSD 3-Clause
- Upstream: <https://github.com/ulikunitz/xz>

Builds that include xz must carry its unmodified upstream `LICENSE`. On every
platform it is embedded in the executable by `internal/legal` and shown under
Settings, which is how the text reaches the user; the texts are generated from
the modules actually compiled into the binary, so a dependency that ships no
license file fails the build. Packagers targeting other platforms must either
embed the text the same way or include the unmodified license file in the
application bundle, package, or accompanying documentation.

## github.com/ebitengine/purego

- Purpose: bindings to documented macOS system frameworks without CGo
- Version: v0.11.1 (from `go.mod`)
- License: Apache License 2.0
- Upstream: <https://github.com/ebitengine/purego>

GoFlasher uses PureGo in its macOS native adapter. PureGo contains code derived from the Go runtime; those portions
are covered by the Go project's BSD 3-Clause license.

Builds containing PureGo must carry:

- `THIRD_PARTY_NOTICES.md`
- `THIRD_PARTY_NOTICES.zh-TW.md`
- PureGo's unmodified upstream `LICENSE`
- The Go project's `LICENSE` covering the Go-runtime-derived portions

All four are embedded in the executable and shown under Settings on every
platform. Packages additionally install GoFlasher's own license as a file,
because the distribution policies require one: Debian and RPM packages place it
at `/usr/share/doc/goflasher/copyright`, Arch packages at
`/usr/share/licenses/goflasher/LICENSE`, and the macOS bundle at
`Contents/Resources/legal/LICENSE`.

## wimlib / libwim

- Version: 1.14.5, built unmodified from the upstream archive
- Purpose: open and split WIM files; linked statically into the Linux, macOS
  and Windows GUI executable through cgo
- Library license: dual GPL-3.0-or-later or LGPL-2.1-or-later (the LGPL option
  applies because the build excludes ntfs-3g); GoFlasher uses the
  LGPL-2.1-or-later option. Some library files are under the MIT license
  (Eric Biggers; libdivsufsort-lite by Yuta Mori).
- Upstream: <https://wimlib.net>
- Compliance record: `docs/legal/wimlib-1.14.5` (approved archive hashes,
  build metadata, dependency report, license inventory and texts,
  Corresponding Source and relinking instructions, approval)

The classification does not reclassify GoFlasher or the operating system
libraries libwim links against. A package that links wimlib ships the
compliance record, including wimlib's license texts, beside the executable,
and its release attaches the wimlib source archive. Reverse engineering
GoFlasher for debugging modifications to libwim is permitted. The release
workflows link wimlib only into archives whose SHA-256 is listed in the
record; a build without wimlib reports the Windows-installer builder as
unavailable.

## UEFI components — PROHIBITED

GoFlasher does not bundle a UEFI implementation, firmware, shim, bootloader, or
UEFI development package. UEFI remains non-MVP. No such component may enter a
release until the exact source snapshot and binary have been reviewed for
`GPL-2.0-only`, `GPL-2.0-or-later`, applicable linking/syscall/firmware/runtime
exceptions, embedded components, GPL-3.0 compatibility, and all distribution
obligations. The release gate rejects candidate payloads containing known UEFI
component names. Files read from a user-supplied ISO are input data and are not
part of the GoFlasher application package.
