# Epoch

Experimental single-operator temporal test runner for stateful Linux applications.
Epoch cold-boots a Firecracker microVM from a private disk copy, changes the
guest's actual wall clock, runs declared unprivileged workload actions, evaluates
JSON observations, and retains evidence with independent execution, assertion and
cleanup outcomes. The host clock and its normal synchronization remain unchanged.

**Validation boundary:** the target host is bare-metal Rocky Linux x86_64. The Go
engine and guest agent are implemented; see the status matrix for test evidence.
A successful real guest boot, vsock session and guest-clock experiment have not
yet been verified. This is not a claim of
production readiness, deterministic execution or hostile-tenant isolation.

## Build and test

The pinned toolchain is Go 1.26.8. Production binaries build with CGO disabled.
Dependency versions and checksums are in go.mod/go.sum; downloading them is an
explicit development/preparation step. Compiled runtime binaries work offline.

```sh
go mod download
go test ./...
go vet ./...
bash scripts/check-architecture.sh
bash scripts/build.sh
```

Build and run these commands on the Rocky Linux host. Race tests require a
working C compiler: `go test -race ./...`. Guest clock tests inject a fake setter.
Ordinary tests never boot KVM or change any actual clock.

## Operator workflow

Use the preserved [host prerequisites](doc/prerequisites.md), then prepare an
explicit local image according to [the guest recipe](docs/guest.md). The repository
does not contain or automatically acquire a guest kernel/rootfs.
Create the gitignored `epoch.local.json` from `configs/epoch.example.json` with
your local paths. Its private runtime root and persistent evidence root must be
separate, non-nested directories. The runtime root must be short enough for the
per-run Unix socket. `~/` is expanded; other paths resolve against the config file.

```sh
bin/epoch version
bin/epoch doctor --config epoch.local.json --json
bin/epoch validate scenarios/clock-smoke.json --config epoch.local.json
bin/epoch run scenarios/clock-smoke.json --config epoch.local.json
bin/epoch report RUN_ID --config epoch.local.json
bin/epoch recover RUN_ID --config epoch.local.json
```

`doctor` is read-only. Its `--probe` flag explicitly creates/releases an empty KVM
VM with no guest memory or vCPU. `validate` checks the complete scenario, workload
declarations, local image metadata and file hashes without booting anything.
`run` is foreground and supports optional `--json` output. Recovery inspects by
default; `--apply` explicitly cleans verified owned resources without replaying an
action. Read the [operator runbook](docs/operations.md) before hardware tests.

## Review map

- [Implementation contract](docs/implementation-contract.md) and
  [evidence/status matrix](docs/implementation-status.md).
- [Architecture and decisions](docs/architecture.md), including ownership and
  readiness boundaries.
- [Scenarios and exact typed assertions](docs/scenarios.md).
- [Guest protocol, privileges and image preparation](docs/guest.md).
- [Host supervision and recovery limits](docs/host-runtime.md).
- [Results, quotas and durability](docs/evidence.md).

The only workload is the independent clock-probe system-test fixture. Subscription
and non-AI knowledge-management examples are deferred. The shell bootstrap remains
separate from the Go runtime; no administration, network setup, sudo or dependency
fetching occurs in `epoch run`.
