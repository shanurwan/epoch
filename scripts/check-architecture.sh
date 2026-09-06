#!/usr/bin/env bash
# Run from the repository root; no binaries or KVM are executed.
set -euo pipefail
deps=$(GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go list -deps ./cmd/epoch)
while IFS= read -r dependency; do
    if [[ "$dependency" == github.com/shanurwan/epoch/internal/guestclock ]]; then
        printf 'FAIL: host CLI imports the guest clock setter\n' >&2
        exit 1
    fi
done <<< "$deps"
printf 'PASS: Linux host dependency graph excludes guestclock\n'
