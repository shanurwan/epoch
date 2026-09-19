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

guest_unit=build/guest/epoch-agent.service
grep -qx 'NoNewPrivileges=yes' "$guest_unit"
grep -qx 'CapabilityBoundingSet=CAP_SYS_TIME CAP_SETUID CAP_SETGID CAP_KILL' "$guest_unit"
grep -qx 'AmbientCapabilities=CAP_SETUID' "$guest_unit"
printf 'PASS: guest credential transition retains only CAP_SETUID as ambient\n'
