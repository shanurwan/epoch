# Implementation status

Target: first experimentally validated single-host release on bare-metal Rocky
Linux x86_64. The engine is implemented; the release acceptance gate is still
pending a prepared disposable image and opt-in guest boot/clock experiments.
Ordinary tests and a working doctor command do not establish that gate.

| Area | Implemented | Locally tested on Rocky | MicroVM hardware verified |
| --- | --- | --- | --- |
| Existing shell bootstrap | Preserved | Prior authoring checks; no administration rerun | Prior user-reported empty-KVM access only |
| Strict scenarios, planning and exact assertions | Yes | Pass, including malformed input and chronology | Pending |
| CLI version/doctor/validate/run/report/recover | Yes | Builds; real version/doctor pass; lifecycle uses explicit fake VMM boundaries | Pending |
| Host privilege and clock observations | Yes | Real ordinary UID, capability inspection, clock reads and comparison tests pass | Guest isolation pending |
| Private artifacts/copies/locking | Yes | Hash mismatch, cancellation, base preservation, ownership and lock tests pass | Real image copy/boot pending |
| Firecracker supervision and parent death | Yes | Real owned subprocess, TERM/KILL, pidfd and three controller-SIGKILL tests pass | Firecracker crash-safety experiment pending |
| Vsock handshake and wire identity | Yes | Real Unix-socket protocol tests pass, including actual hello readiness contract | AF_VSOCK guest session pending |
| Guest clock guards and readback | Yes | Fake syscall boundary only; no actual setter used | Pending |
| Guest actions/services and fault fixtures | Yes | Real subprocess tests pass, including descendants, races, deadlines, nonzero exits and output quotas | Guest privilege/init checks pending |
| Evidence, outcome precedence and recovery | Yes | Pass, including host-discontinuity simulation, quota drain, failed publication and interrupted recovery | Abrupt VM/controller experiment pending |
| Offline guest-image helper and systemd recipe | Yes | Bash syntax and read-only candidate inspection pass | Privileged preparation and boot pending |
| Explicit KVM test harness and nine scenarios | Yes | Scenario validation, Bash syntax and precise-report predicates pass | Not executed |

## Recorded checks

On the reference Rocky host as ordinary UID 1000 with Go 1.26.8:

- `CGO_ENABLED=0 go test -count=1 -timeout 120s ./...`: pass.
- `go vet ./...`: pass.
- `bash scripts/check-architecture.sh`: pass; host graph excludes guestclock.
- `bash scripts/build.sh`: pass; Linux amd64 host, agent and fixture binaries.
- `epoch version`: 0.1.0-dev, go1.26.8, linux/amd64.
- Read-only `epoch doctor --json`: pass; SELinux enforcing, cgroup v2, ordinary
  KVM access and exact Firecracker v1.16.1 executable identity. No probe requested.
- Final Firecracker/engine regression tests pass after accommodating the actual
  version command's bounded diagnostic tail.
- Action/service response allowance and parent-deadline/cancellation tests pass.
  The declared workload timeout is unchanged; bounded response time cannot extend
  the overall run deadline.
- Cancellation during guest shutdown and VMM cleanup is preserved in the final
  report; five regression cases pass, including incomplete-cleanup precedence.
- The harness accepts nine synthetic expected reports and rejects 18 unrelated
  failure variants, including VM crashes, lost responses and missing clock data.
  These checks do not boot a guest or establish hardware acceptance.

The Linux source dependency scan with govulncheck v1.7.0 found no known
vulnerabilities across the application, pinned modules and Go 1.26.8 standard
library (database timestamp 2026-09-02T19:12:04Z). This is point-in-time tooling
evidence, not a security certification. Race instrumentation is pending a working
C compiler on the reference host; none was found in the inspected PATH.

## Preparation evidence

Go 1.26.8 was installed into an ordinary-user tool directory after checking the
published archive SHA-256. No system package, profile, network, time-service or
SELinux changes were made. The original bootstrap remains separate.

The original lab contained Firecracker and image-catalogue metadata, with no
prepared guest. Candidate kernel 6.1.155 and Ubuntu 24.04 squashfs inputs were then
selected from actual published v1.15 object keys, downloaded during explicit
preparation and hashed again on Rocky. See artifacts/guest-inputs.candidate.lock.json.
Firecracker itself remains v1.16.1; no VMM downgrade occurred. These are candidates,
not boot-verified images. Their observed hashes are not upstream signatures.

Read-only checks confirmed kernel ELF64 x86-64, built-in vsock/virtio block/ext4/
devtmpfs support, valid squashfs, real systemd and empty pseudo-filesystem input
directories. The privileged image helper has not been executed. No KVM guest,
real clock setter or opt-in hardware harness has been run by this implementation.

Reference host kernel 5.14.0-687.42.1.el9_8.x86_64 remains a lab compatibility
target outside Firecracker v1.16.1's upstream host validation matrix. No claim of
upstream certification, reproducibility, deterministic execution, enterprise
readiness or production availability follows from these checks.
