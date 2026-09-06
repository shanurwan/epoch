# Epoch working rules

Epoch is an experimental single-operator temporal test runner. Read
`docs/implementation-contract.md` and `docs/implementation-status.md` first.

- Preserve user edits, the existing bootstrap and licence. Do not commit or push.
- Host code never sets clocks, timezone or NTP. Never test a real clock setter on
  the development host. `cmd/epoch` must not depend on `internal/guestclock`.
- Refuse root or active CAP_SYS_TIME in host runtime. Guest clock control requires
  both the boot marker and the configured non-host vsock CID.
- No sudo, administration, image formatting, VM boot or guest clock changes without
  explicit operator opt-in. Runtime is offline, ordinary-user and has no guest NIC.
- Only private copied disks and verified owned processes/paths may be cleaned up.
- Treat lost action responses as unknown outcomes. Never retry business actions.
- Keep execution, assertions and cleanup outcomes separate. Record validation
  boundaries honestly; fake tests do not demonstrate KVM or guest clock isolation.

Build/test (pinned Go 1.26.8): `go test ./...`, `go vet ./...`;
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./cmd/epoch ./cmd/epoch-agent`.
Race tests require a working C compiler: `go test -race ./...`.
Hardware tests use the explicit opt-in runner `scripts/test-kvm.sh` only.
