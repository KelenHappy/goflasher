#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 4 ]]; then
  echo "usage: $0 OWNER/REPOSITORY TAG ASSET OUTPUT" >&2
  exit 2
fi

repository=$1
tag=$2
asset_name=$3
output=$4

if [[ ! $repository =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
  [[ ! $tag =~ ^[A-Za-z0-9._-]+$ ]] ||
  [[ ! $asset_name =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "invalid GitHub release asset identifier" >&2
  exit 2
fi

metadata=$(mktemp)
download=$(mktemp "${output##*/}.XXXXXX")
trap 'rm -f "$metadata" "$download"' EXIT

gh api "repos/$repository/releases/tags/$tag" >"$metadata"
mapfile -t asset < <(
  jq -r --arg name "$asset_name" \
    '.assets[] | select(.name == $name) | [.id, .digest] | @tsv' "$metadata"
)

if [[ ${#asset[@]} -ne 1 ]]; then
  echo "expected exactly one $asset_name asset in $repository release $tag" >&2
  exit 1
fi

IFS=$'\t' read -r asset_id digest <<<"${asset[0]}"
if [[ ! $asset_id =~ ^[0-9]+$ ]] || [[ ! $digest =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "release asset is missing a valid GitHub SHA-256 digest" >&2
  exit 1
fi

gh api -H 'Accept: application/octet-stream' \
  "repos/$repository/releases/assets/$asset_id" >"$download"
printf '%s  %s\n' "${digest#sha256:}" "$download" | sha256sum --check --status
install -m 0755 "$download" "$output"
