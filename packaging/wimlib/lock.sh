#!/usr/bin/env bash

load_wimlib_lock() {
  if [[ $# -ne 1 || ! -f "$1" ]]; then
    echo "wimlib lock file is missing" >&2
    return 65
  fi

  local line key value flag
  local seen_WIMLIB_VERSION=
  local seen_WIMLIB_SOURCE=
  local seen_WIMLIB_SOURCE_URL=
  local seen_WIMLIB_SOURCE_SHA256=
  local seen_LINUX_TOOLCHAIN=
  local seen_MACOS_TOOLCHAIN=
  local seen_CONFIGURE_FLAGS=

  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* ]] && continue

    if [[ "$line" != *=* ]]; then
      echo "invalid wimlib lock entry: $line" >&2
      return 65
    fi

    key=${line%%=*}
    value=${line#*=}

    case "$key" in
      WIMLIB_VERSION)
        [[ -n "$seen_WIMLIB_VERSION" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_WIMLIB_VERSION=1
        WIMLIB_VERSION=$value
        ;;
      WIMLIB_SOURCE)
        [[ -n "$seen_WIMLIB_SOURCE" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_WIMLIB_SOURCE=1
        WIMLIB_SOURCE=$value
        ;;
      WIMLIB_SOURCE_URL)
        [[ -n "$seen_WIMLIB_SOURCE_URL" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_WIMLIB_SOURCE_URL=1
        WIMLIB_SOURCE_URL=$value
        ;;
      WIMLIB_SOURCE_SHA256)
        [[ -n "$seen_WIMLIB_SOURCE_SHA256" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_WIMLIB_SOURCE_SHA256=1
        WIMLIB_SOURCE_SHA256=$value
        ;;
      LINUX_TOOLCHAIN)
        [[ -n "$seen_LINUX_TOOLCHAIN" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_LINUX_TOOLCHAIN=1
        LINUX_TOOLCHAIN=$value
        ;;
      MACOS_TOOLCHAIN)
        [[ -n "$seen_MACOS_TOOLCHAIN" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_MACOS_TOOLCHAIN=1
        MACOS_TOOLCHAIN=$value
        ;;
      CONFIGURE_FLAGS)
        [[ -n "$seen_CONFIGURE_FLAGS" ]] && {
          echo "duplicate wimlib lock key: $key" >&2
          return 65
        }
        seen_CONFIGURE_FLAGS=1
        CONFIGURE_FLAGS=$value
        ;;
      *)
        echo "unknown wimlib lock key: $key" >&2
        return 65
        ;;
    esac
  done <"$1"

  for key in \
    WIMLIB_VERSION \
    WIMLIB_SOURCE \
    WIMLIB_SOURCE_URL \
    WIMLIB_SOURCE_SHA256 \
    LINUX_TOOLCHAIN \
    MACOS_TOOLCHAIN \
    CONFIGURE_FLAGS
  do
    eval "value=\${$key}"
    if [[ -z "$value" ]]; then
      echo "missing wimlib lock value: $key" >&2
      return 65
    fi
  done

  [[ "$WIMLIB_SOURCE" == "wimlib-$WIMLIB_VERSION.tar.gz" ]] || {
    echo "wimlib source filename does not match version" >&2
    return 65
  }

  [[ "$WIMLIB_SOURCE_URL" == https://* ]] || {
    echo "wimlib source URL must use HTTPS" >&2
    return 65
  }

  [[ "${WIMLIB_SOURCE_URL##*/}" == "$WIMLIB_SOURCE" ]] || {
    echo "wimlib source URL does not match filename" >&2
    return 65
  }

  [[ "$WIMLIB_SOURCE_SHA256" =~ ^[0-9a-f]{64}$ ]] || {
    echo "invalid wimlib source SHA-256" >&2
    return 65
  }

  read -r -a WIMLIB_CONFIGURE_FLAGS <<<"$CONFIGURE_FLAGS"

  ((${#WIMLIB_CONFIGURE_FLAGS[@]} > 0)) || {
    echo "missing wimlib configure flags" >&2
    return 65
  }

  for flag in "${WIMLIB_CONFIGURE_FLAGS[@]}"; do
    [[ "$flag" =~ ^--[a-z0-9][a-z0-9-]*(=.*)?$ ]] || {
      echo "invalid wimlib configure flag: $flag" >&2
      return 65
    }
  done
}
