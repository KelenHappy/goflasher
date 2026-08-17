#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 ]]; then
  echo "usage: $0 OWNER/REPOSITORY IMMUTABLE_TAG ASSET OUTPUT EXPECTED_SHA256" >&2
  exit 2
fi

repository=$1
tag=$2
asset_name=$3
output=$4
expected_sha256=$5

if [[ ! $repository =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] ||
  [[ ! $tag =~ ^[A-Za-z0-9._-]+$ ]] ||
  [[ ! $asset_name =~ ^[A-Za-z0-9._-]+$ ]] ||
  [[ ! $expected_sha256 =~ ^[0-9a-f]{64}$ ]]; then
  echo "invalid GitHub release asset identifier or expected SHA-256" >&2
  exit 2
fi
if [[ $tag == continuous || $tag == latest ]]; then
  echo "mutable release tag is forbidden: $tag" >&2
  exit 2
fi

metadata=$(mktemp)
download=$(mktemp "${output##*/}.XXXXXX")
trap 'rm -f "$metadata" "$download"' EXIT

gh api "repos/$repository/releases/tags/$tag" >"$metadata"
mapfile -t asset_ids < <(
  jq -r --arg name "$asset_name" '.assets[] | select(.name == $name) | .id' "$metadata"
)
if [[ ${#asset_ids[@]} -ne 1 || ! ${asset_ids[0]} =~ ^[0-9]+$ ]]; then
  echo "expected exactly one $asset_name asset in $repository release $tag" >&2
  exit 1
fi

gh api -H 'Accept: application/octet-stream' \
  "repos/$repository/releases/assets/${asset_ids[0]}" >"$download"
printf '%s  %s\n' "$expected_sha256" "$download" | sha256sum --check --status
install -m 0755 "$download" "$output"
