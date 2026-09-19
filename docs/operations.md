# Operator runbook

## Before a run

Use the ordinary operator account on bare-metal Linux x86_64. Prepare host access
separately with the existing [prerequisite guide](../doc/prerequisites.md). The
runtime refuses root and active CAP_SYS_TIME, preserves SELinux, never invokes
sudo and leaves host clock synchronization enabled. Rocky 9.8 kernel
5.14.0-687.42.1.el9_8.x86_64 is a lab compatibility target outside Firecracker
v1.16.1's upstream validation matrix.

Prepare the pinned Go toolchain and dependencies before offline use. Optional
`scripts/prepare-go.sh` installs Go 1.26.8 into a new user-local directory after
checking the published archive hash. It does not change system packages or PATH.
Put that directory's go/bin on PATH for your build shell, then use scripts/build.sh.

Copy configs/epoch.example.json to gitignored epoch.local.json and set paths to
trusted local Firecracker v1.16.1, image manifests, private runtime root and
persistent evidence root. The roots must be separate and private; the generated
vsock path must fit the conservative 90-byte limit. `doctor` inspects without KVM
creation; `doctor --probe` is the explicit empty-VM operation.

The image manifest file is IMAGE_ID.json in artifact_manifest_dir. Kernel/rootfs
paths and workload manifest paths resolve relative to that file. The engine
validates hashes, sizes, architecture, pinned VMM version, agent identity, workload
declarations and recipe metadata before launching. The image recipe emits the
explicitly selected `IMAGE_ID.json`; documented examples are
`epoch-clock-probe-v1` and `epoch-agent-authority-expiry-v1`. Each prepared image
contains one declared workload. No prepared image is shipped or silently downloaded.

## Hardware opt-in

Read scripts/build-guest-image.sh and docs/guest.md before separately authorizing
administrator image preparation. It operates on staged regular files only and is
never called by the engine or ordinary tests. Review the local userspace/kernel
source identity and the generated manifest before treating the image as disposable.

Only after explicit operator authorization for KVM boots and guest clock changes:

```sh
bash scripts/test-kvm.sh --allow-disposable-guest-clock \
  /absolute/epoch/bin/epoch /absolute/epoch.local.json
```

The harness validates the image, then runs forward/backward clock, persistence,
real wait, foreground service and expected-nonzero cases plus execution-failure
controls. It checks all three result dimensions, exact exit codes and the expected
failed action's guest error details. A boot failure, lost response or VM crash is
not an acceptable substitute for a fixture's expected error. It neither
installs images nor changes host policy. A successful harness run proves only the
listed experiments on that exact host/image/build combination. Retain and review
machine evidence outside Git. Boot compatibility, cgroup containment, hostile
workloads and power-failure behavior are not inferred from ordinary tests.

## Failure and recovery

Ctrl-C/SIGTERM cancels the run. Cleanup gets a separate bounded context to request
guest shutdown, terminate/reap the owned VMM and remove its disk/socket. If death
cannot be established, disk cleanup remains INCOMPLETE. No global process killing
or arbitrary recursive deletion is used.

After controller death, use `epoch report RUN_ID --config epoch.local.json` and
`epoch recover RUN_ID --config epoch.local.json`. The latter is dry-run. It checks
host boot ID, UID and process start identity and uses a pidfd for recovery signals.
An ambiguous launch record or reused PID requires manual review; no signal is sent.
Unknown runtime files are retained rather than guessed to be disposable.

Use `recover ... --apply` only for the inspected run. It never repeats a workload
action. After a reboot, the old PID is not acted upon; persistent ownership
evidence can be inspected even if the ephemeral runtime root disappeared. If a
record or evidence file is unreadable, retain it and inspect the underlying storage
failure. Do not make a new run by deleting an unresolved ownership record.

## Exit semantics

| Command | Exit codes |
| --- | --- |
| run | 0 pass; 1 completed assertion failure; 2 invocation/scenario/config; 3 execution/environment/interruption; 4 incomplete cleanup; 130 cancellation after cleanup |
| validate | 0 validated; 2 invocation/config/scenario/artifact validation error |
| doctor | 0 checks/probe passed; 2 invocation/config error; 3 host/tool/probe error |
| report | 0 valid final report displayed, independent of its verdict; 2 invocation/config/ID error; 3 unavailable/invalid/partial report |
| recover | 0 inspection permits cleanup or apply succeeded; 2 invocation/config/ID error; 4 unresolved ownership/cleanup error |
| version | 0 displayed; 2 invalid arguments |

Incomplete cleanup takes precedence over execution/cancellation, then assertion
failure. All retained causes remain available. `report` exit 0 does not mean the
reported experiment passed. Text rendering escapes untrusted control characters.
