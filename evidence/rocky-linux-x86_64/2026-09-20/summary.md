# Hardware verification: Rocky Linux x86_64, 2026-09-20

This evidence verifies Epoch's current cold-boot execution path at Git revision
`5f0f89f03f0660e511dee13e49a28b105aea7684`. It does not verify snapshot-backed
forking or deterministic execution of arbitrary Linux systems.

## Test host

```text
Rocky Linux       9.8 (Blue Onyx)
Kernel            5.14.0-687.42.1.el9_8.x86_64
Architecture      x86_64, bare metal
CPU               12th Gen Intel Core i7-1265U
KVM               accessible; empty-VM probe passed
Firecracker       1.16.1
Go                1.26.8 linux/amd64
Epoch revision    5f0f89f03f0660e511dee13e49a28b105aea7684
```

## Verified path

| Gate | Result | Representative run |
| --- | --- | --- |
| Firecracker start and guest boot | VERIFIED | `a132d09fe7cbfdd6949b0aedb1ace68d` |
| Authenticated guest-agent/vsock communication | VERIFIED | `a132d09fe7cbfdd6949b0aedb1ace68d` |
| Guest-only wall-clock adjustment and readback | VERIFIED | `a132d09fe7cbfdd6949b0aedb1ace68d` |
| Declared workload execution as UID 10001 | VERIFIED | `a132d09fe7cbfdd6949b0aedb1ace68d` |
| Typed assertions and persistent final evidence | VERIFIED | all four representative runs |
| Owned runtime cleanup | VERIFIED | all four representative runs |

## Temporal boundary experiment

```text
Temporal case A
  guest target      2035-01-01T12:00:29Z
  operation         worker.restart(worker-17)
  authority         AUTHORIZED
  side effect       true; restart_count=1
  assertion         PASS

Temporal case B
  guest target      2035-01-01T12:00:31Z
  operation         worker.restart(worker-17)
  authority         DENY_EXPIRED
  side effect       false
  assertion         PASS

TOCTOU
  request target    2035-01-01T12:00:25Z
  initial check     AUTHORIZED
  execution target  2035-01-01T12:00:35Z
  execution check   DENY_EXPIRED
  side effect       false; restart_count=0
  assertion         PASS
```

The authority interval is `not_before <= now < expires_at`. The two boundary
cases use the same declared authority, operation, resource, workload, software,
and image identity. They are independent cold boots from equivalent declared
inputs, not identical-memory snapshot forks.

## Repeatability

Twenty fresh runs were executed for each of the before-expiry, after-expiry, and
TOCTOU scenarios. All 60 runs completed with execution `COMPLETED`, assertions
`PASS`, and cleanup `COMPLETE`. Each scenario produced exactly one normalized
semantic-result hash across its 20 runs. Normalization excludes run IDs,
timestamps, and durations; it retains the declared temporal targets, typed
decisions, side effects, workload state, assertions, and outcome dimensions.

This supports a scoped statement: these tested scenarios produced repeatable
normalized semantic observations and assertion outcomes on the recorded host.
It does not establish general deterministic Linux execution.

## Host-clock safety

Before and after the experiments, the host reported NTP active, system clock
synchronized, and chrony leap status `Normal`. The ordinary Epoch operator had
no active or ambient `CAP_SYS_TIME`. Epoch did not set the host clock or alter its
time-synchronization configuration; real host time progressed normally.

## Measured aggregate timings

The 60 authority runs produced these directly observed nearest-rank statistics:

| Phase | Count | Min | p50 | p95 | Max |
| --- | ---: | ---: | ---: | ---: | ---: |
| Private disk/runtime preparation | 60 | 2.140 s | 2.150 s | 2.215 s | 2.237 s |
| VMM start + guest boot + vsock readiness | 60 | 705.9 ms | 714.8 ms | 715.8 ms | 721.5 ms |
| Guest-reported clock apply/readback | 80 | 24.1 us | 42.9 us | 51.1 us | 64.8 us |
| Epoch `action.exec` step | 100 | 1.881 ms | 3.913 ms | 5.145 ms | 6.008 ms |
| Cleanup to finished | 60 | 79.2 ms | 87.4 ms | 98.9 ms | 102.6 ms |
| Total run | 60 | 2.962 s | 2.979 s | 3.041 s | 3.074 s |

Firecracker launch versus guest readiness, assertion evaluation, evidence
persistence, and final report-write latency are not separately instrumented and
are not estimated.

## Evidence handling

The files in this directory are compact, sanitized extracts. Raw run reports,
event streams, clock observations, Firecracker logs, host pre/post captures, and
all 60 normalized repeat records remain in the owner's private lab evidence
directory. Guest images and binaries are intentionally excluded from Git; their
SHA-256 identities are recorded in `manifest.json`.
