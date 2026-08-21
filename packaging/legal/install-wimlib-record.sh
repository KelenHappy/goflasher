#!/usr/bin/env bash
set -euo pipefail
[[ $# -eq 1 ]] || { echo "usage: $0 DESTINATION" >&2; exit 64; }
[[ -n "${WIMLIB_COMPLIANCE_RECORD:-}" && -d "$WIMLIB_COMPLIANCE_RECORD" ]] || { echo "approved WIMLIB_COMPLIANCE_RECORD required" >&2; exit 65; }
dest=$1; mkdir -p "$dest/licenses"
cp -R "$WIMLIB_COMPLIANCE_RECORD/licenses/." "$dest/licenses/"
for item in source.sha256 artifact.sha256 build-metadata.txt dependencies.txt license-inventory.txt corresponding-source.txt LEGAL_APPROVED; do
  install -m644 "$WIMLIB_COMPLIANCE_RECORD/$item" "$dest/$item"
done
