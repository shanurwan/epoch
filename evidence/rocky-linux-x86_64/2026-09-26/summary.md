# Stateful reference verification: Rocky Linux x86_64, 2026-09-26

This evidence exercises Epoch's isolated incident-response MCP/PostgreSQL
reference application on bare-metal Rocky Linux. It is **working-tree evidence**:
the source is Git revision `710e94bf2d427a38aa25f9bdcf7706ba427f38b0` plus
an uncommitted source-affecting patch whose SHA-256 is
`bf4ad9d09fa34ff1a02934acbaf6aa1b362e7741f8f2f3e7db935309b3f8441e`.
It must not be represented as clean-revision evidence.

This verifies the recorded cold-boot executions only. It does not verify
snapshot-backed forking or deterministic execution of arbitrary Linux systems.

## Test host

```text
Rocky Linux       9.8 (Blue Onyx)
Kernel            5.14.0-687.42.1.el9_8.x86_64
Architecture      x86_64, bare metal
CPU               12th Gen Intel Core i7-1265U
KVM               accessible; empty-VM probe passed
Firecracker       1.16.1
Go                1.26.8 linux/amd64
Base revision     710e94bf2d427a38aa25f9bdcf7706ba427f38b0
Source state      base revision plus recorded uncommitted patch
```

## Verified path

| Gate | Result | Evidence |
| --- | --- | --- |
| Firecracker start and guest boot | VERIFIED | all accepted runs |
| Authenticated guest-agent/vsock communication | VERIFIED | all accepted runs |
| Guest-only wall-clock adjustment and readback | VERIFIED | all accepted runs |
| PostgreSQL 16 initialization and baseline reset | VERIFIED | all accepted runs |
| Official MCP Go SDK over stdio | VERIFIED | tool discovery and typed call results |
| Signed authority and execution-time revalidation | VERIFIED | all three representative runs |
| Database transaction and denial invariants | VERIFIED | `0 -> 1`, `0 -> 0`, and audit rows |
| Typed Epoch assertions and final evidence | VERIFIED | all accepted runs |
| Owned runtime cleanup | VERIFIED | all accepted runs; no Firecracker process remained |

## Temporal boundary experiment

```text
Epoch Stateful Temporal Boundary Experiment

Temporal case A
  guest target      2035-01-01T12:00:20Z
  MCP tool          worker_restart(payments-worker-17)
  initial check     AUTHORIZED
  execution check   AUTHORIZED
  PostgreSQL        restart_count 0 -> 1; health unhealthy -> healthy
  side effect       true
  assertion         PASS

Temporal case B
  guest target      2035-01-01T12:00:31Z
  MCP tool          worker_restart(payments-worker-17)
  initial check     DENY_EXPIRED
  PostgreSQL        restart_count 0 -> 0; health remains unhealthy
  side effect       false
  assertion         PASS

Real TOCTOU
  guest target      2035-01-01T12:00:25Z
  initial check     12:00:25.031Z AUTHORIZED
  real delay        7005 ms observed on the guest wall clock
  execution check   12:00:32.036Z DENY_EXPIRED
  PostgreSQL        restart_count 0 -> 0; health remains unhealthy
  side effect       false
  assertion         PASS
```

The authority interval is `not_before <= now < expires_at`. Each accepted run
cold-booted a private disk from the same declared image, reset PostgreSQL to the
same synthetic baseline, and used actual guest wall-clock reads. These are
independent temporal executions, not identical-memory snapshot forks.

## Repeatability

Twenty fresh runs were executed for each scenario. All 60/60 completed with
execution `COMPLETED`, assertions `PASS`, and cleanup `COMPLETE`. Each scenario
produced exactly one normalized semantic-result hash across its 20 runs.
Normalization retains MCP discovery/call order, typed decisions, database state,
audit outcome, assertions, and cleanup while excluding volatile identifiers,
timestamps, paths, and durations.

This supports a scoped statement: these three stateful temporal scenarios
produced repeatable normalized semantic observations and assertion outcomes on
the recorded host. It does not establish general deterministic Linux execution.

## Host-clock safety

Before and after the experiments, the host reported NTP active, system clock
synchronized, and chrony leap status `Normal`. The ordinary Epoch operator had
no active `CAP_SYS_TIME`. Epoch did not set the host clock or change host time
synchronization; real host time progressed normally.

## Failure retained during hardware bring-up

The first image run, `582249b7e351e5ec740894f236f5bcb8`, failed guest
readiness because the unprivileged PostgreSQL unit attempted to reopen
`/dev/console` and systemd returned `209/STDOUT`. Cleanup completed. The accepted
image inherits PID 1's already-open output streams instead; it does not grant the
database access to the console device. The provenance JSON also required a jq
variable rename because `module` is reserved by the installed jq parser. Both
changes are present in the recorded source-affecting patch.

## Evidence handling

The files in this directory are compact, sanitized extracts. Raw reports, event
streams, clock observations, Firecracker logs, signed repository metadata,
package payloads, host captures, and all 60 normalized records remain in the
owner's private lab evidence directory. Guest disks, binaries, JWTs, signing
keys, and SSH information are excluded from Git.
