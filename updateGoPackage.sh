#!/usr/bin/env bash
set -euo pipefail

go clean -modcache
go get -u ./...
go mod tidy
