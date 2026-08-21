# Release compliance record

This record is evaluated against the actual candidate payload, not merely the
source repository. `packaging/legal/verify-release.sh` is the executable gate.

| Component | Version/artifact | License classification | GPL-3.0 compatibility | Obligations/status |
|---|---|---|---|---|
| GoFlasher | candidate commit | GPL-3.0 | n/a | Include GPL text and Corresponding Source |
| ulikunitz/xz | v0.5.16, compiled Go module | BSD-3-Clause | compatible | Preserve copyright/license; packaged |
| purego | v0.10.2, compiled Go module | Apache-2.0 plus Go-derived BSD-3-Clause portions | compatible with GPLv3 distribution; component terms remain distinct | Include Apache and Go license texts; Linux and macOS |
| wimlib/libwim | intended 1.14.4; no approved release artifact recorded | **UNCONFIRMED** | **UNCONFIRMED** | Release blocker: source, headers, licenses, dependency graph, notices, Corresponding Source/relink analysis and legal approval absent |
| native transitive dependencies of libwim | no final artifact dependency report | **UNCONFIRMED** | **UNCONFIRMED** | Release blocker; classify each actual dependency independently |
| UEFI component | none permitted | not evaluated | not evaluated | Non-MVP; payload prohibited by gate |
| Remaining compiled Go modules | exact final build graph not yet attached | **UNCONFIRMED component-by-component** | **UNCONFIRMED** | Release blocker: generate graph, collect each license/notice, classify, and compare with payload |

## Provenance and source availability

- Go module versions and sums: `go.mod` and `go.sum`.
- GoFlasher build scripts: repository `packaging/` and workflows.
- Intended wimlib recipe: `packaging/wimlib/BUILD.lock` and `build.sh`.
- Approved wimlib source archive/commit, source SHA-256, binary SHA-256,
  patches, actual feature list, dependency inventory, license bundle, legal
  approval, and verified Corresponding Source location: **UNCONFIRMED/MISSING**.

Consequently an installer-capable release is blocked. A raw-writer package may
ship only after the final-payload gate confirms that it contains no libwim or
UEFI component, includes the notices/licenses for every component it contains,
and consumes a separately reviewed release-compliance record. No legal advice
or final legal conclusion is asserted here.
