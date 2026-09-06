# Host runtime boundary

Epoch is an experimental single-operator temporal test runner. The Linux x86_64
runtime runs as the ordinary operator. It neither invokes a host shell for
scenario steps nor changes host clocks, timezone, synchronisation, networking,
SELinux policy, or cgroup delegation. Existing host administration remains in
`scripts/bootstrap-host.sh` and is separate from runtime execution.

## Admission and observations

The runtime refuses UID 0 and inspects every currently visible `/proc/self/task`
thread for real, effective, saved and filesystem UID 0 and CAP_SYS_TIME in its
permitted, effective and ambient sets. An unreadable, incomplete, malformed, or
disappearing record fails closed. A broad capability bounding set alone is not
active privilege. Epoch does not mutate credentials; new Go threads inherit
their creator's credentials. These observations do not defend against malicious
same-UID peers or an operator modifying the program.

Host observations read CLOCK_REALTIME, CLOCK_MONOTONIC and CLOCK_BOOTTIME.
Comparisons use the monotonic delta as the elapsed-time reference: a realtime
delta disagreement detects a sampled wall-clock discontinuity; a boottime delta
disagreement detects suspend or another environmental clock anomaly. The default
250 ms threshold is a conservative short-lab-run policy, not a benchmark. Normal
NTP remains enabled. Scheduling delays during sequential clock reads can also
make a sample inconclusive. A detected anomaly is an environmental error and
does not identify its cause or prove Epoch changed the host clock.

Doctor checks SELinux enforcement, cgroup v2, KVM device type and ordinary access.
Only explicit `--probe` requests KVM_GET_API_VERSION and creates/closes an empty
VM descriptor. It installs no guest memory, vCPUs or guest code. A context is
checked around each operation; a wedged kernel ioctl cannot be forcibly unwound
by a Go context. The CLI must remain an ordinary process that the operator can
inspect and terminate. Default doctor does not issue any KVM ioctl.

## Files and executable identity

Runtime and evidence roots are operator-owned directories with exact mode 0700.
Evidence/configuration files use mode 0600. Existing unsafe modes are rejected,
not silently changed. Path checks reject symlinks, unclean/relative paths, foreign
ownership, and group/other-writable components. A root-owned sticky temporary
ancestor is accepted; the private root itself still requires operator ownership
and 0700. Malicious same-UID filesystem races are outside this initial profile.

The VMM executable is a regular, trusted file with no setuid/setgid bits or file
capabilities. Epoch records absolute path, SHA-256, size, device, inode and the
bounded `--version` response. The first complete stdout line must exactly match
the pinned version; preceding noise or a different version is rejected. The
v1.16.1 executable also emits an exit diagnostic after that line. Complete stdout
and stderr are recorded separately, each bounded to 4096 bytes; extra diagnostic
lines do not change the version identity, and exceeding either quota is an error.
Launch reopens and rechecks identity. Both version
inspection and launch execute the verified open read-only descriptor through
`/proc/self/fd/3`; argv[0] records the actual absolute artifact path. This avoids
silently executing a replacement pathname after inspection. It is content
identity, not a signature or a hostile-operator defence.

The only VM launch mode is `--no-api --config-file`. The generated configuration
has one private writable root disk, one vsock device with CID 3, no network
device, the real init target `epoch.target`, and explicit run/nonce/CID boot
markers. The vsock Unix path is limited to 90 bytes to leave headroom below the
Linux socket address limit. Default Firecracker seccomp remains enabled. Direct
launch is a lab limitation; jailer hardening and delegated host cgroup limits are
deferred. Machine-config RAM/vCPUs are not full host-resource containment.

## Child lifetime and recovery

One dedicated goroutine calls `runtime.LockOSThread`, starts the VMM with Linux
`Pdeathsig: SIGKILL`, and remains on the creating thread until its sole `Wait`
finishes. This accounts for the Linux parent-death signal being attached to the
creating thread, not merely the Go process. Go's Linux fork/exec implementation
also records the pre-fork parent PID, installs PR_SET_PDEATHSIG, then compares
getppid and self-signals on disagreement; this handles the setup race. The pinned
toolchain implementation must retain this behavior when upgraded. Relevant
upstream behavior is documented in [Go SysProcAttr](https://pkg.go.dev/syscall#SysProcAttr)
and [Go issue 27505](https://go.dev/issue/27505).

Ordinary stop requests TERM, then KILL after one second if required, and waits
for that owned child. Output collectors have a separate bounded drain period.
A stop deadline expiring before reaping is incomplete cleanup, never permission
to delete a potentially live disk. The parent-death tests use disposable helper
processes and controller SIGKILL; they perform no guest boot or clock operation.
Passing them establishes process behavior on the tested host, not full KVM crash
safety under power loss, storage faults, or a wedged kernel.

Recovery compares host boot ID, operator UID, PID and `/proc/PID/stat` start ticks.
An old boot ID means that recorded process cannot survive. A PID identity
mismatch is ambiguous and requires manual review. Recovery opens a pidfd,
rechecks identity, and sends signals through that stable handle only. It refuses
to fall back to numeric-PID signals if pidfds are unavailable. Zombie processes
are already dead; their remaining entry belongs to the reaper, not a live VM.
Creation and manifest publication are distinct operations: abrupt death can
occur between them. Inspection must retain ambiguous resources and never resume
a business action automatically.

The operator lock is an advisory exclusive flock held on one private file; its
inode is retained when unlocked. Atomic JSON uses a same-directory temporary
file, fsync, rename, and directory fsync on Linux. It does not promise survival
through arbitrary storage faults. Free-space checks use available filesystem
blocks against the full copy budget plus reserve, with no sparse-file discount.
