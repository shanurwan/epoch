# Implementation status

Epoch's first documented single-host hardware acceptance path has completed on a
bare-metal Rocky Linux x86_64 host. The accepted evidence maps to clean Git
revision `5f0f89f03f0660e511dee13e49a28b105aea7684` and exact hashes for the Epoch
binary, guest agent, workloads, scenarios, Firecracker binary, guest kernel,
base userspace, and prepared root filesystems.

This acceptance is deliberately scoped. It verifies the recorded cold-boot
clock-smoke and authority-expiry paths on one host. It does not establish
production readiness, hostile-tenant isolation, snapshot-backed forks, upstream
host certification, or deterministic execution of arbitrary Linux systems.

| Area | Implemented | Software-tested | Reference-host hardware status |
| --- | --- | --- | --- |
| Strict scenarios, planning and exact assertions | Yes | Pass, including malformed input and chronology | VERIFIED for four recorded scenarios |
| CLI version/doctor/validate/run/report/recover | Yes | Pass | Version, doctor, probe, validate, run and report VERIFIED; recovery apply not exercised |
| Host privilege and clock observations | Yes | Pass | Ordinary operator had no active `CAP_SYS_TIME`; pre/post NTP and chrony state retained |
| Private artifacts, copies and locking | Yes | Pass, including ownership and cancellation | Private image copy and owned successful-run cleanup VERIFIED |
| Firecracker supervision and parent death | Yes | Pass with owned subprocess, TERM/KILL, pidfd and controller-SIGKILL tests | Normal start/stop VERIFIED; crash-safety remains software-tested only |
| Vsock handshake and wire identity | Yes | Pass with real Unix-socket protocol tests | Authenticated AF_VSOCK guest session VERIFIED |
| Guest clock guards and readback | Yes | Pass with fake syscall boundary | Real guest `CLOCK_REALTIME` change and readback VERIFIED |
| Guest actions and services | Yes | Pass, including descendants, races, deadlines, nonzero exits and quotas | Real systemd guest and UID 10001 workload execution VERIFIED |
| Agent-authority-expiry reference workload | Yes | Unit and boundary tests pass | Before, after and execution-time revalidation scenarios VERIFIED |
| Isolated incident-response MCP/PostgreSQL reference application | Yes | Demo-module authority, handler, structured MCP and deterministic-decision tests pass | VERIFIED in a recorded working-tree hardware run; not yet clean-revision evidence |
| Evidence, outcome precedence and recovery | Yes | Pass, including quota, publication and interruption cases | Final evidence and independent successful outcomes VERIFIED; abrupt interruption remains software-tested only |
| Offline guest-image helper and systemd recipe | Yes | Syntax and safety checks pass | Two workload-specific images prepared and booted successfully |
| Explicit nine-scenario clock-probe KVM harness | Yes | Predicates and failure variants pass | Full harness NOT TESTED; the smaller `clock-smoke.json` gate passed |

## Recorded checks

The exact revision was built on Rocky with Go 1.26.8:

- `go mod download`: pass.
- `go test ./...`: pass.
- `go vet ./...`: pass.
- `bash scripts/check-architecture.sh`: pass; the host graph excludes
  `guestclock`.
- `bash scripts/build.sh`: pass; Linux amd64 host, agent and workload binaries.
- `git diff --check`: pass.
- `go test -race ./...`: not run because the inspected host had no C compiler.

`epoch doctor --probe --json` reported ordinary UID 1000, SELinux enforcing,
cgroup v2, readable/writable group-based KVM access, a successful empty-KVM probe,
and exact Firecracker 1.16.1 executable identity. The normal runtime remained an
ordinary-user foreground process and did not use the jailer.

The accepted representative runs were:

| Scenario | Run ID | Execution | Assertions | Cleanup |
| --- | --- | --- | --- | --- |
| Clock smoke | `a132d09fe7cbfdd6949b0aedb1ace68d` | `COMPLETED` | `PASS` | `COMPLETE` |
| Authority before expiry | `17a24e3cb125188db33cf99cc9472fde` | `COMPLETED` | `PASS` | `COMPLETE` |
| Authority after expiry | `47ebb5402063e9e3b298d7e5a0a383e2` | `COMPLETED` | `PASS` | `COMPLETE` |
| Authority TOCTOU | `251a079ec8db9f5d0206fab1358623ea` | `COMPLETED` | `PASS` | `COMPLETE` |

The clock-smoke run cold-booted the guest, completed the authenticated guest-agent
handshake over vsock, moved the guest wall clock across the 1999/2000 boundary,
ran the declared clock-probe as UID 10001, preserved workload-local state across
the clock step, evaluated typed assertions, published the final report, and
removed owned runtime resources.

The authority runs observed `AUTHORIZED` with a simulated side effect before
expiry, `DENY_EXPIRED` without a side effect after expiry, and a TOCTOU transition
from an accepted `AUTHORIZED` request to `DENY_EXPIRED` at the later execution
boundary with restart count zero.

The separate `demos/incident-response-agent` application is not part of the clean
`5f0f89f` acceptance above. A later working-tree exercise based on revision
`710e94bf2d427a38aa25f9bdcf7706ba427f38b0` plus validated code-patch SHA-256
`bf4ad9d09fa34ff1a02934acbaf6aa1b362e7741f8f2f3e7db935309b3f8441e`
verified its guest startup, PostgreSQL initialization, MCP calls, database
transitions, audits, assertions, and cleanup. It remains working-tree evidence
until those changes are committed and rerun from that clean revision.

The representative stateful runs were:

| Scenario | Run ID | Execution | Assertions | Cleanup |
| --- | --- | --- | --- | --- |
| MCP/PostgreSQL before expiry | `e2d0eaf44f343f01824e24fd74ab1f6d` | `COMPLETED` | `PASS` | `COMPLETE` |
| MCP/PostgreSQL after expiry | `7084f7cfe211a5527a9e17042e7b3b54` | `COMPLETED` | `PASS` | `COMPLETE` |
| MCP/PostgreSQL real TOCTOU | `1442d841957a3e3db60f086a455636f9` | `COMPLETED` | `PASS` | `COMPLETE` |

Before expiry, the database changed `restart_count` from zero to one and worker
health from `unhealthy` to `healthy`. After expiry, both remained unchanged. In
the real TOCTOU run, actual guest-clock checks moved from `AUTHORIZED` at
`12:00:25.031Z` to `DENY_EXPIRED` at `12:00:32.036Z` after 7005 ms, immediately
before the denied transaction.

## Scoped repeatability and timings

Twenty new cold-boot executions were run for each authority scenario. All 60/60
completed with execution `COMPLETED`, assertions `PASS`, and cleanup `COMPLETE`.
For each scenario, all 20 normalized semantic observations produced one SHA-256
value. The normalization retains declared temporal targets, decisions, side
effects, workload state, assertions, and independent outcome dimensions while
excluding volatile run IDs, timestamps, and durations.

This supports the statement that the tested temporal scenarios produced
repeatable normalized semantic observations and assertion outcomes across 20
executions per scenario under the recorded configuration. It does not support a
general deterministic-execution guarantee.

Measured values are reported only where the existing event model exposes them.
Across the 60 authority runs, median total duration was 2.979 seconds and p95 was
3.041 seconds. The combined Firecracker-start, guest-boot, vsock-ready phase had a
714.8 ms median and 715.8 ms p95. Firecracker launch versus guest readiness,
assertion latency, evidence-persistence latency, and final report-write latency
are not separately instrumented and are not estimated.

The stateful MCP/PostgreSQL scenarios were also repeated 20 times each. All
60/60 runs completed with execution `COMPLETED`, assertions `PASS`, and cleanup
`COMPLETE`; each scenario produced one normalized semantic hash. Median total
duration was 4.612 seconds before expiry, 4.626 seconds after expiry, and 11.699
seconds for the deliberate seven-second TOCTOU case. The combined Firecracker
launch, guest boot, PostgreSQL initialization, guest-agent readiness, and vsock
handshake had medians between 1.214 and 1.215 seconds. See the recorded
performance JSON for min, median, p95, max, and instrumentation limits.

## Host-clock safety

The host wall clock progressed normally throughout the experiment. Before and
after the representative and repetition runs, `timedatectl` reported NTP active
and the system clock synchronized; `chronyc tracking` reported leap status
`Normal`. The ordinary operator's current and ambient capability sets were empty,
so the process did not possess active host `CAP_SYS_TIME`. No host time-setting or
time-synchronization change was part of the procedure.

## Evidence and remaining limits

The compact [hardware evidence](../evidence/rocky-linux-x86_64/2026-09-20/summary.md)
contains the source/artifact manifest, representative semantic outcomes,
repeatability summary, and measurements. Raw reports, event streams, clock
observations, Firecracker logs, host captures, and per-run normalized records are
retained privately outside Git. Large images and binaries are not committed.

The later [stateful reference evidence](../evidence/rocky-linux-x86_64/2026-09-26/summary.md)
uses the same compact convention and records its non-clean working-tree provenance.

Exploratory runs before the accepted revision exposed a missing ambient
`CAP_SETUID` in the guest service and an empty-envelope mismatch in the TOCTOU
workload. Those failures remain in the private evidence set; the accepted images
and all reported pass counts use the exact final revision and hashes above.

The Rocky host kernel `5.14.0-687.42.1.el9_8.x86_64` remains outside Firecracker
1.16.1's upstream validation matrix. Direct non-jailer execution, lack of host
cgroup quotas, hostile workloads, abrupt VM/controller failure on real hardware,
power-loss durability, snapshot restore, and multi-branch orchestration remain
outside the verified boundary. Epoch is not production-ready.
