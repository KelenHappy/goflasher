#!/usr/bin/env bash
set -euo pipefail

# Exits 0 when EXECUTABLE has wimlib linked in. The marker is the linkage
# constant in internal/wim/native/link_wimlib.go, which is compiled only with
# the wimlib build tag. LC_ALL=C makes grep match raw bytes in the binary.

[[ $# -eq 1 ]] || { echo "usage: $0 EXECUTABLE" >&2; exit 64; }
test -f "$1" || { echo "executable not found: $1" >&2; exit 66; }
LC_ALL=C grep -qaF 'statically linked wimlib' "$1"
