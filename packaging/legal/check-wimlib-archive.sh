#!/usr/bin/env bash
set -euo pipefail

# Refuses a libwim.a whose SHA-256 is not listed in the approved compliance
# record. Run it before linking: the final payload no longer contains the
# archive as a separate file, so verify-release.sh cannot hash it later.

[[ $# -eq 1 ]] || { echo "usage: $0 LIBWIM_ARCHIVE" >&2; exit 64; }
test -f "$1" || { echo "libwim archive not found: $1" >&2; exit 66; }
record=${WIMLIB_COMPLIANCE_RECORD:-}
[[ -n "$record" && -d "$record" ]] || { echo "approved WIMLIB_COMPLIANCE_RECORD required" >&2; exit 65; }
test -s "$record/artifact.sha256" || { echo "incomplete libwim compliance record: artifact.sha256" >&2; exit 65; }
# Hash stdin: given a path with a backslash (Windows $RUNNER_TEMP), GNU
# sha256sum escapes the name and prefixes the line with "\".
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum <"$1" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 <"$1" | awk '{print $1}')
else
  echo "no SHA-256 implementation available" >&2; exit 69
fi
grep -Fqx "$actual" "$record/artifact.sha256" || { echo "libwim artifact hash not approved: $actual" >&2; exit 65; }
echo "Approved libwim archive: $actual"
