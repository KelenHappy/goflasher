#!/usr/bin/env bash
set -euo pipefail

# Downloads the wimlib source pinned in BUILD.lock into DEST_DIR, checks its
# SHA-256, and prints the tarball path.

if [[ $# -ne 1 ]]; then
  echo "usage: $0 DEST_DIR" >&2
  exit 64
fi

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)

# Parse BUILD.lock as data. Never source the lock file itself.
# shellcheck disable=SC1091
source "$root/packaging/wimlib/lock.sh"
load_wimlib_lock "$root/packaging/wimlib/BUILD.lock"

mkdir -p "$1"
source_path="$(cd "$1" && pwd)/$WIMLIB_SOURCE"

curl \
  --fail \
  --silent \
  --show-error \
  --location \
  --proto '=https' \
  --tlsv1.2 \
  "$WIMLIB_SOURCE_URL" \
  -o "$source_path"

if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$source_path" | awk '{print $1}')
else
  actual=$(shasum -a 256 "$source_path" | awk '{print $1}')
fi

if [[ "$actual" != "$WIMLIB_SOURCE_SHA256" ]]; then
  rm -f "$source_path"
  echo "wimlib source hash mismatch" >&2
  echo "expected: $WIMLIB_SOURCE_SHA256" >&2
  echo "actual:   $actual" >&2
  exit 65
fi

printf '%s\n' "$source_path"
