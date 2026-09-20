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

- Purpose: dynamic-library bridge for bundled libwim on Linux and macOS, and
  bindings to documented macOS system frameworks without CGo
- Version: v0.10.2 (from `go.mod`)
- License: Apache License 2.0
- Upstream: <https://github.com/ebitengine/purego>

GoFlasher uses PureGo in both Linux and macOS libwim bridges and in its macOS
native adapter. PureGo contains code derived from the Go runtime; those portions
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

## wimlib / libwim — NOT YET CLEARED FOR RELEASE

- Intended version: 1.14.5
- Intended purpose: open and split WIM files through the PureGo bridge
- Library license: LGPL-2.1-or-later
- Compatibility: compatible with distribution as part of GoFlasher under
  GPL-3.0, subject to the artifact-specific conditions below

The LGPL-2.1-or-later classification applies to the wimlib 1.14.5 library; it
does not reclassify GoFlasher, optional wimlib components, or native transitive
dependencies. Before a libwim artifact may ship, the release record must verify
the exact bundled artifact against the exact source snapshot, every applicable
source-header and license/notice file, build configuration, enabled optional
features, and every linked or bundled native transitive dependency. Each
transitive component retains its own license and obligations.

The release record must also contain the source and binary SHA-256 values,
toolchain and linker versions, flags, downstream patches, dependency report,
license texts, legal approval, and a tested Corresponding Source offer/location
including build/install scripts and any material needed for relinking where
applicable. The package must preserve LGPL notices and license text, permit
replacement of the shared library, and must not prohibit reverse engineering
for debugging modifications to the library. Until those facts and obligations
are verified against the actual release artifact, libwim distribution is
blocked; GoFlasher must report the Windows builder as unavailable.

## UEFI components — PROHIBITED

GoFlasher does not bundle a UEFI implementation, firmware, shim, bootloader, or
UEFI development package. UEFI remains non-MVP. No such component may enter a
release until the exact source snapshot and binary have been reviewed for
`GPL-2.0-only`, `GPL-2.0-or-later`, applicable linking/syscall/firmware/runtime
exceptions, embedded components, GPL-3.0 compatibility, and all distribution
obligations. The release gate rejects candidate payloads containing known UEFI
component names. Files read from a user-supplied ISO are input data and are not
part of the GoFlasher application package.
