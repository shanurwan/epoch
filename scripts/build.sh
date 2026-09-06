#!/usr/bin/env bash
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
export GOTOOLCHAIN=local
[[ $(go env GOVERSION) == go1.26.8 ]] || { printf 'Go 1.26.8 is required; prepare it explicitly.\n' >&2; exit 1; }
mkdir -p bin
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
go build -mod=readonly -trimpath -o bin/epoch ./cmd/epoch
go build -mod=readonly -trimpath -o bin/epoch-agent ./cmd/epoch-agent
go build -mod=readonly -trimpath -o bin/clock-probe ./workloads/clock-probe
printf 'Built Linux amd64 binaries in bin/. No binary was executed.\n'
