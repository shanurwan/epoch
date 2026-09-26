#!/usr/bin/env bash
set -euo pipefail
umask 077

usage() {
  printf '%s\n' 'Usage: build-binaries.sh OUTPUT_DIR' >&2
}
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ $# -eq 1 ]] || { usage; exit 2; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Linux x86_64 is required'
[[ $1 == /* && ! -e $1 && ! -L $1 ]] || fail 'OUTPUT_DIR must be a new absolute path'
parent=$(realpath -e -- "$(dirname -- "$1")")
output=$parent/$(basename -- "$1")
[[ $output == "$1" ]] || fail 'OUTPUT_DIR must be canonical without traversal'
mkdir -m 0700 -- "$output"

demo_dir=$(realpath -e -- "$(dirname -- "${BASH_SOURCE[0]}")/..")
cd "$demo_dir"
export GOTOOLCHAIN=local
[[ $(go env GOVERSION) == go1.26.8 ]] || fail 'Go 1.26.8 is required; prepare it explicitly'
export CGO_ENABLED=0 GOOS=linux GOARCH=amd64
for entry in \
  agent:incident-response-agent \
  mcp-server:incident-ops-mcp \
  authority-issuer:incident-authority-issuer \
  postgres-bootstrap:incident-postgres-bootstrap; do
  command_name=${entry%%:*}
  output_name=${entry#*:}
  go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w' -o "$output/$output_name" "./cmd/$command_name"
done
sha256sum -- "$output"/* > "$output/SHA256SUMS"
printf 'Built Linux x86_64 demo binaries at %s\n' "$output"
