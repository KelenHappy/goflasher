#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 1 ]] || { echo "usage: $0 EXTRACTED_RELEASE_ROOT" >&2; exit 64; }
payload=$(realpath "$1"); test -d "$payload"
if [[ "${GOFLASHER_RELEASE_GATE:-0}" = 1 ]]; then
  record=${RELEASE_COMPLIANCE_RECORD:-}
  [[ -d "$record" ]] || { echo "release compliance record missing" >&2; exit 65; }
  for item in component-inventory.txt dependency-inventory.txt source-hashes.txt artifact-hashes.txt corresponding-source.txt package-notice-audit.txt LEGAL_APPROVED; do
    test -s "$record/$item" || { echo "release compliance record incomplete: $item" >&2; exit 65; }
  done
  grep -Fxq APPROVED "$record/LEGAL_APPROVED" || { echo "release legal approval missing" >&2; exit 65; }
fi
find_one() { find "$payload" -type f -name "$1" -print -quit; }
for notice in THIRD_PARTY_NOTICES.md THIRD_PARTY_NOTICES.zh-TW.md; do
  test -n "$(find_one "$notice")" || { echo "missing $notice" >&2; exit 65; }
done
# UEFI is non-MVP. Inspect payload names, not user ISO contents processed later.
if find "$payload" -type f \( -iname '*edk2*' -o -iname '*gnu-efi*' -o -iname '*uefi-shell*' -o -iname 'shim*.efi' \) -print -quit | grep -q .; then
  echo "prohibited UEFI component in release payload" >&2; exit 65
fi
lib=$(find "$payload" -type f \( -name 'libwim.so.15' -o -name 'libwim.15.dylib' \) -print -quit)
if [[ -n "$lib" ]]; then
  record=${WIMLIB_COMPLIANCE_RECORD:-}
  [[ -d "$record" ]] || { echo "libwim present without compliance record" >&2; exit 65; }
  for item in source.sha256 artifact.sha256 build-metadata.txt dependencies.txt license-inventory.txt corresponding-source.txt LEGAL_APPROVED; do
    test -s "$record/$item" || { echo "incomplete libwim compliance record: $item" >&2; exit 65; }
  done
  grep -Fxq APPROVED "$record/LEGAL_APPROVED" || { echo "libwim legal approval missing" >&2; exit 65; }
  grep -Fq 'LGPL-2.1-or-later' "$record/license-inventory.txt" || { echo "libwim LGPL-2.1-or-later classification missing" >&2; exit 65; }
  grep -Eiq 'replacement|relink' "$record/corresponding-source.txt" || { echo "libwim replacement/relink instructions missing" >&2; exit 65; }
  actual=$(shasum -a 256 "$lib" | awk '{print $1}')
  grep -Fqx "$actual" "$record/artifact.sha256" || { echo "libwim artifact hash not approved" >&2; exit 65; }
  test -d "$record/licenses" && test -n "$(find "$record/licenses" -type f -print -quit)" || { echo "libwim license payload missing" >&2; exit 65; }
  packaged_licenses=$(find "$payload" -type f \( -path '*/Resources/legal/wimlib-*/*' -o -path '*/share/doc/goflasher/third-party/wimlib-*/*' \) -print -quit)
  test -n "$packaged_licenses" || { echo "libwim notices/licenses absent from final payload" >&2; exit 65; }
fi
