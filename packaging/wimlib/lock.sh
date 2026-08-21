#!/usr/bin/env bash

# load_wimlib_lock parses BUILD.lock as a strict key/value data file.  The lock
# is never evaluated as shell code.
load_wimlib_lock() {
  if [[ $# -ne 1 || ! -f "$1" ]]; then
    echo "wimlib lock file is missing" >&2
    return 65
  fi

  local line key value flag
  declare -A seen=()
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ "$line" != *=* ]]; then
      echo "invalid wimlib lock entry: $line" >&2
      return 65
    fi
    key=${line%%=*}
    value=${line#*=}
    case "$key" in
      WIMLIB_VERSION|WIMLIB_SOURCE|WIMLIB_SOURCE_URL|WIMLIB_SOURCE_SHA256|LINUX_TOOLCHAIN|MACOS_TOOLCHAIN|CONFIGURE_FLAGS) ;;
      *) echo "unknown wimlib lock key: $key" >&2; return 65 ;;
    esac
    if [[ ${seen[$key]+set} ]]; then
      echo "duplicate wimlib lock key: $key" >&2
      return 65
    fi
    seen[$key]=1
    printf -v "$key" '%s' "$value"
  done <"$1"

  for key in WIMLIB_VERSION WIMLIB_SOURCE WIMLIB_SOURCE_URL WIMLIB_SOURCE_SHA256 LINUX_TOOLCHAIN MACOS_TOOLCHAIN CONFIGURE_FLAGS; do
    if [[ ! ${seen[$key]+set} || -z ${!key} ]]; then
      echo "missing wimlib lock value: $key" >&2
      return 65
    fi
  done
  [[ "$WIMLIB_SOURCE" == "wimlib-$WIMLIB_VERSION.tar.gz" ]] || { echo "wimlib source filename does not match version" >&2; return 65; }
  [[ "$WIMLIB_SOURCE_URL" == https://* ]] || { echo "wimlib source URL must use HTTPS" >&2; return 65; }
  [[ "${WIMLIB_SOURCE_URL##*/}" == "$WIMLIB_SOURCE" ]] || { echo "wimlib source URL does not match filename" >&2; return 65; }
  [[ "$WIMLIB_SOURCE_SHA256" =~ ^[0-9a-f]{64}$ ]] || { echo "invalid wimlib source SHA-256" >&2; return 65; }

  read -r -a WIMLIB_CONFIGURE_FLAGS <<<"$CONFIGURE_FLAGS"
  ((${#WIMLIB_CONFIGURE_FLAGS[@]} > 0)) || { echo "missing wimlib configure flags" >&2; return 65; }
  for flag in "${WIMLIB_CONFIGURE_FLAGS[@]}"; do
    [[ "$flag" =~ ^--[a-z0-9][a-z0-9-]*(=.*)?$ ]] || { echo "invalid wimlib configure flag: $flag" >&2; return 65; }
  done
}
