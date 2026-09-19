# Epoch

**Temporal Forking Infrastructure for Deterministic Batch & Expiry Testing**

Epoch is general-purpose infrastructure for testing software whose behavior changes
with wall-clock time: scheduled batches, settlement windows, billing cycles,
subscriptions, credentials, certificates, sessions, retention windows, TTLs,
retries, leases, maintenance windows, delayed execution, and delegated authority.
It creates controlled temporal executions without changing the host clock or its
normal time synchronization.

The current implementation cold-boots one Firecracker microVM from a fresh private
disk copy, sets the guest's actual wall clock, runs explicitly declared unprivileged
workload actions, evaluates typed JSON observations, and retains evidence with
independent execution, assertion, and cleanup outcomes. These are independent
temporal executions from equivalent declared image inputs. They are not
Firecracker memory-snapshot forks and do not begin from identical RAM/device state.

The project thesis uses “deterministic” as the target for controlled batch and
expiry testing. Deterministic execution has not yet been established by hardware
evidence. See [Temporal forking](docs/temporal-forking.md) for the precise current
capability and roadmap.

**Validation boundary:** the target host is bare-metal Rocky Linux x86_64. The Go
engine and guest agent are implemented; see the status matrix for test evidence.
A successful real guest boot, vsock session and guest-clock experiment have not
yet been verified. This is not a claim of
production readiness, deterministic execution or hostile-tenant isolation.

## Core and reference workloads

Epoch core knows only scenario declarations, guest time control, declared workload
actions, observations, assertions, evidence, and owned cleanup/recovery. Domain
semantics live in replaceable guest workloads:

```text
Epoch core
  +-- clock-probe                  system-test fixture
  +-- agent-authority-expiry       one expiry/TOCTOU reference experiment
  +-- future scheduled-batch
  +-- future subscription-expiry
  +-- future certificate-expiry
```

The flagship reference experiment asks what happens when an incident-response
software agent is authorized to perform a simulated `worker.restart`, but the
authority expires before the execution boundary. It is local, deterministic at
the application-test level, requires no LLM, network, container platform, secrets
service, or external authorization product, and never restarts a host service.
See [Expiring autonomous-agent authority](docs/reference-agent-authority-expiry.md).

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
- [Temporal-forking terminology and roadmap](docs/temporal-forking.md).
- [Scenarios and exact typed assertions](docs/scenarios.md).
- [Authority-expiry reference experiment](docs/reference-agent-authority-expiry.md).
- [Guest protocol, privileges and image preparation](docs/guest.md).
- [Host supervision and recovery limits](docs/host-runtime.md).
- [Results, quotas and durability](docs/evidence.md).

The shell bootstrap and offline image preparation remain separate from the Go
runtime; no administration, network setup, sudo, package installation, dependency
fetching, or image download occurs in `epoch run`.
