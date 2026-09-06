# Epoch implementation contract

Status: proposed implementation contract for the first experimentally validated,
single-host release. See `implementation-status.md` for actual evidence. The
product label is **experimental single-operator temporal test runner**.

## Product and boundary

Epoch is an application-independent Go CLI and execution engine for isolated
temporal regression tests of stateful Linux applications in Firecracker/KVM.
The module is `github.com/shanurwan/epoch`; C is deferred. Subscription/batch/expiry
and non-AI knowledge management/search are later, separate reference workloads.
They do not belong in the core. Examples are not validated customers or compliance
claims. No career goals belong in project documentation.

From a validated local scenario and prepared images, the ordinary-user foreground
controller cold-boots one microVM, establishes actual guest wall time, runs declared
unprivileged workload operations, evaluates observations, records evidence, and
cleans up its owned resources. Runtime is offline. No NIC, TAP, bridge, firewall,
SSH server, cloud service, host command execution or host filesystem passthrough.
Guest loopback is permitted. Each run receives a fresh full private disk copy.

The target is Linux x86_64 on bare-metal Rocky Linux, one operator/VM at a time,
trusted local images and synthetic workloads. Deferred: daemon/HTTP API,
Kubernetes, scheduling, snapshots/restore, frozen/accelerated monotonic time,
instruction replay, external databases, timezone/DST and leap seconds, billing,
LLMs, secrets, arbitrary hostile code, delegated cgroup enforcement. Guest machine
configuration is not a hard host RSS/CPU quota. Direct non-jailer launch is a lab
limitation, not hardened tenant isolation or enterprise/production readiness.

## Safety invariants

H1. Host code never changes a clock, timezone, NTP or host time services.
H2. Refuse host runtime as UID 0 or with CAP_SYS_TIME effective, permitted or ambient;
fail closed if inspection fails. A bounding set alone is not active privilege.
H3. The host dependency graph cannot import guestclock. Architecture checks enforce
this. Clock-setter unit tests exercise fake syscall boundaries only.
H4. Guest setters require the explicit Epoch kernel boot marker and verified local
vsock CID matching the non-host configured CID. These are accidental-execution
guards, not attestation. A nonce is correlation, not authentication.
H5. A real init system owns PID 1. An explicit offline-prepared guest target
supervises the agent; no generic host systemctl configuration commands.
H6. No guest time daemon or workload before readiness. Workload processes run as
a designated non-root guest UID/GID without clock capability.
H7. Never hard-link or write the base image. No block devices or host mounts.
H8. Private trusted roots, owner checks, 0700 directories and 0600 files; reject
symlink/path traversal; safe generated IDs; bounded Unix socket paths. Malicious
same-UID peers are outside this profile.
H9. Preserve SELinux and default Firecracker seccomp. Reject setuid/file-capability
VMM executables; record exact executable content and version; absolute argv, no
shell. Normal runtime never downloads or elevates privileges.
H10. Cleanup only verified run-owned paths/processes. No pkill, global deletion,
stale-PID signals or unchecked recursive deletion. Retain disks if a VM may live.

Administration stays in the preserved `scripts/bootstrap-host.sh`. Python probes
are development/bootstrap aids, not a second controller. No sudo, host security,
network/time changes, physical formatting or privileged image preparation without
explicit operator approval. Hardware tests require explicit opt-in and a verified
disposable microVM. Do not execute a real setter on the authoring host.

## Time and observations

Mode: `guest-wall-clock-step/v1`. Explicit-offset RFC3339 targets normalize to UTC;
1990-01-01T00:00:00Z <= target < 2100-01-01T00:00:00Z. CLOCK_REALTIME is set and then
ticks normally. Sample realtime/monotonic immediately around setting and validate
local readback with elapsed time and recorded tolerance (default 1000ms). A delayed
host receipt is not compared against an unadjusted target. No EPOCH_NOW or mocked
date executable in the integration path.

Every step records both clocks. Run/step contexts and waits use elapsed time, not
the simulated calendar. CLOCK_MONOTONIC is not intentionally jumped. Serialized
Go time values do not preserve their in-process monotonic component. Initial time
precedes workloads; later changes are explicit. Running services require
clock.set allow_live=true. Jumps neither execute missed jobs nor undo state or RAM;
recovery operations are explicit. No universal application expiry semantics.

Sample host realtime, monotonic and boottime at boundaries and around guest clock
operations. A configurable default 250ms delta discontinuity/suspend threshold
stops the experiment as environmental ERROR/inconclusive, not an application
defect or evidence Epoch caused a host adjustment. Leave NTP enabled. Sampling
plus privilege controls supports a bounded claim, not universal proof.

## Transport and workload

One Firecracker launch path: `--no-api --config-file`. Configuration contains boot
source, private disk, machine config, vsock, and the run marker. Host connects to a
private Unix socket and sends `CONNECT 7000\n`; validate bounded `OK <port>\n` where
the returned port is host-assigned. Preserve buffered bytes. Guest CID is 3.
Serial is diagnostic only. Control is bounded NDJSON with version, request ID,
operation, run ID and nonce echoed in responses; typed result or typed error.

Only hello, clock.read, clock.set, action.exec, service.start, service.stop and
shutdown are wire operations; waits are host sequencing. Every request has a
deadline. Readiness retries observe VMM exit and a boot deadline. Never retry a
state-changing request after a lost response; classify unknown outcome ERROR.
Version/identity/JSON/connection errors are platform errors.

Image-embedded workload metadata defines ID/version, fixed argv, non-root UID/GID,
working directory, allowed environment and readiness actions. Requests select IDs
and provide bounded JSON stdin, never shell interpolation or expected assertions.
Actions emit required JSON stdout and diagnostic stderr. Normal nonzero exits are
observations compared against expected_exit_code (default zero). Launch failure,
missing/malformed required stdout, lost responses and timeouts are execution errors.

Foreground services use owned process groups, readiness deadlines, bounded logs,
unexpected-death detection and cleanup. Long requests cannot block cancellation
indefinitely. No daemonising services. Initial independent clock-probe fixture
reads OS realtime/monotonic and persists a marker. Deterministic fault fixtures
cover sleep/hang, output overflow, malformed JSON and nonzero exit. Later examples
capture time once per application decision and use safe integration margins.

## Validation and policy

JSON only. Strict unknown/duplicate key, trailing data, version, operation, type,
ID, interval, duration, count and observation-reference validation. Semantic
validation finishes before any launch. Machine paths live in gitignored
epoch.local.json; portable scenarios resolve image IDs through local metadata.
Assertions: eq/ne/gt/gte/lt/lte/exists/absent using RFC6901 JSON Pointer with exact
numeric comparisons and preserved types. No eval or shell expressions. Missing
required observations are execution errors. At least one explicit assertion is
required for a passing verdict, alongside automatic exit and time checks.

Conservative design defaults (not benchmarks): 1 vCPU/512 MiB, maxima 2/2048;
private disk <=4 GiB; free space reserves at least 5 GiB beyond a full byte copy;
30s boot, 120s run, 600s maximum; actions bounded by remaining context. Full copies
are context-aware with atomic publication, no reflinks or snapshots. ENOSPC fails.
Do not mistake sparse apparent size for available storage.

1 MiB frames and scenarios; 64 KiB action input; 128 steps and assertions; four
live services; 64 KiB each stdout/stderr; 4096 events; 8 MiB combined evidence/logs
excluding disk images, with final-report reserve. Quota exhaustion cancels with
an explicit error, never a truncated required observation presented as passing.
Output readers keep draining/discarding until owned processes terminate.

## Artifacts and preparation

Metadata records schema/image ID, architecture, kernel/rootfs source identity,
SHA-256 and size, expected Firecracker version, guest build/protocol identity,
workload digest, recipe revision and transformations. Verify local content before
each run. Published checksums differ from locally observed hashes; hashes are
identities, not signatures/provenance proof if both file and manifest are replaced.
Do not claim bit-for-bit reproducibility without testing it.

Acquisition is explicit preparation, never run-time latest lookup or guessed image
names. The empty v1.16 CI prefix does not authorize silent version substitution.
Accept operator-supplied, recorded compatible artifacts. The optional offline image
helper stages an explicitly supplied userspace, installs the agent, fixture,
unprivileged account and controlled init target into a new regular image file and
validates it. It must never format physical devices or edit host /boot/services.
No binaries, archives, images or machine evidence in Git.

## Lifecycle and recovery

Persist VALIDATING -> PREPARING -> BOOTING -> READY -> RUNNING -> COLLECTING ->
CLEANING -> FINISHED. After creating resources every failure enters CLEANING.
Ownership metadata records run ID, host boot ID, UID, paths, process PID/start
identity, start time and configuration/artifact hashes around side effects;
crashes can occur between action and record. A private exclusive operator lock
rejects simultaneous runs. Incomplete prior runs require inspection/recovery.

Start the VMM with a minimal environment, noninteractive stdin, bounded stdout and
stderr collectors, cleanup registered immediately and Wait called exactly once.
Early exit/EOF is a typed boot failure. SIGINT/SIGTERM cancels; cleanup uses its own
bounded context. Stop agent services where possible, then owned VMM TERM/KILL/reap.
Delete sockets/disks only after death is known. Parent-death handling must account
for Linux creating-thread semantics and the setup race and be tested. Ordinary
process tests do not prove hardware crash safety.

Recovery defaults to dry-run, never replays actions. Verify host boot ID, UID and
start identity; use pidfds when supported and refuse ambiguity. After reboot never
act on the old PID. If a VM may still live, preserve its disk. Controller SIGKILL
or power failure may prevent final reports; partial evidence is INTERRUPTED.

## Evidence and exits

Independent dimensions:
execution_status COMPLETED|ERROR|CANCELLED|INTERRUPTED;
assertion_status PASS|FAIL|NOT_EVALUATED;
cleanup_status COMPLETE|INCOMPLETE.
Success requires COMPLETED, every required assertion evaluated/PASS, and COMPLETE.
Preserve completed assertions after infrastructure failure without calling the
partially exercised experiment passing. Never treat VM crash as an expected defect.

Run exits: 0 pass; 1 completed assertion failure; 2 invocation/config/scenario
validation; 3 execution/environment/interruption; 4 incomplete cleanup; 130 user
cancellation with complete cleanup. Cleanup failure takes precedence, then
cancellation/execution, then assertions. Retain all causes.

Private persistent evidence: scenario.input.json, scenario.resolved.json,
manifest.json, events.ndjson, clock-observations.ndjson, Firecracker output,
guest diagnostics and report.json. Events include schema, sequence, stage,
elapsed monotonic duration and host UTC. Final report publication follows cleanup:
same-directory temp, fsync, rename and directory sync where supported. Rename is
not proof against arbitrary storage faults. Truncated NDJSON stays visibly partial.
Evidence write failure is a storage execution error; never claim a report was saved.

Commands: version; doctor --config [--probe] [--json] (read-only unless explicit
empty-KVM probe); validate SCENARIO --config (no boot); run SCENARIO --config;
report RUN_ID --config [--json]; recover RUN_ID --config [--apply]. All operations
are foreground and local; normal runtime does not fetch dependencies.

## Known evidence

User-reported Rocky 9.8 kernel 5.14.0-687.42.1.el9_8.x86_64, Intel i7-1265U VT-x,
12 logical CPUs, about 30 GiB RAM, XFS /home, SELinux enforcing, cgroup v2, active
normal chronyd. Ordinary kvm-group empty-VM creation/descriptor cleanup succeeded.
Firecracker v1.16.1 executable/version and archive SHA-256
382a02a869e4d6d5cb14c40577f9545e8458021ea8b0b2d3fc10ec14d9c242e6 were checked.
Configurable executable paths are required. The Rocky host kernel is outside the
release validation matrix: lab target, not upstream-supported combination.
Guest boot, vsock, agent and temporal experiments remain separate evidence gates.

Pinned build toolchain: Go 1.26.8, supported release line at implementation time.
Production binaries build CGO-disabled. Standard library is preferred; x/sys
provides Linux primitives and mdlayher/vsock provides the guest transport. Pinned
dependencies are acquired only during explicit development/preparation.

Primary references: [Go releases](https://go.dev/dl/?mode=json),
[Firecracker vsock](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/docs/vsock.md),
[kernel policy](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/docs/kernel-policy.md).
