# Release compliance record

This record is evaluated against the actual candidate payload, not merely the
source repository. `packaging/legal/verify-release.sh` is the executable gate.

| Component | Version/artifact | License classification | GPL-3.0 compatibility | Obligations/status |
|---|---|---|---|---|
| GoFlasher | candidate commit | GPL-3.0 | n/a | Include GPL text and Corresponding Source |
| ulikunitz/xz | v0.5.16, compiled Go module | BSD-3-Clause | compatible | Preserve copyright/license; packaged |
| purego | v0.11.1, compiled Go module | Apache-2.0 plus Go-derived BSD-3-Clause portions | compatible with GPLv3 distribution; component terms remain distinct | Include Apache and Go license texts; Linux and macOS |
| wimlib/libwim | 1.14.5, unmodified, statically linked into the GUI executable; approved `libwim.a` hashes in `docs/legal/wimlib-1.14.5/artifact.sha256` (windows-amd64, linux-amd64, macos-amd64, macos-arm64) | dual GPL-3.0-or-later / LGPL-2.1-or-later, used under LGPL-2.1-or-later; MIT-licensed files inside | compatible | Approved 2026-10-18 (`docs/legal/wimlib-1.14.5/LEGAL_APPROVED`); packages ship the record and license texts, releases attach the source archive |
| native transitive dependencies of libwim | operating system libraries only (`docs/legal/wimlib-1.14.5/dependencies.txt`); ntfs-3g and FUSE disabled | system libraries, not distributed | compatible (system library exception) | None bundled |
| UEFI component | none permitted | not evaluated | not evaluated | Non-MVP; payload prohibited by gate |
| Remaining compiled Go modules | exact final build graph not yet attached | **UNCONFIRMED component-by-component** | **UNCONFIRMED** | Release blocker: generate graph, collect each license/notice, classify, and compare with payload |

## Provenance and source availability

- Go module versions and sums: `go.mod` and `go.sum`.
- GoFlasher build scripts: repository `packaging/` and workflows.
- wimlib recipe: `packaging/wimlib/BUILD.lock` and `build.sh`.
- wimlib compliance record (`WIMLIB_COMPLIANCE_RECORD_PATH`):
  `docs/legal/wimlib-1.14.5` — source and `libwim.a` SHA-256, build metadata,
  dependency report, license inventory and texts, Corresponding Source and
  relinking instructions, and approval. An installer-capable package may ship
  only with a `libwim.a` hash listed there.

A raw-writer package may
ship only after the final-payload gate confirms that its executable does not
link wimlib, that it contains no shared libwim or UEFI component, includes the notices/licenses for every component it contains,
and consumes a separately reviewed release-compliance record. No legal advice
or final legal conclusion is asserted here.
