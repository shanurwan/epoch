# Guest control and offline image preparation

The guest agent and its clock setter are guest-only code. Unit tests inject a
fake clock boundary; no test invokes the real setter on the development host.
The standalone clock-probe reads Linux CLOCK_REALTIME and CLOCK_MONOTONIC directly.
It has no requested-clock environment override and does not import the engine.
The authority-expiry reference workload reads the process wall clock for each
application decision and has no injected runtime time override.

## Boot and privilege boundary

Use a complete local Linux x86_64 userspace with real systemd init and a compatible
local kernel with built-in virtio block, ext4, devtmpfs and vsock. Userspace may be
a reviewed squashfs file or a marked prepared directory. Candidate upstream input
identities are recorded in [guest-inputs.candidate.lock.json](../artifacts/guest-inputs.candidate.lock.json).
They are not a boot-verified Epoch image. No filenames are derived from the
Firecracker release, and preparation does not download anything.
Kernel compatibility, systemd boot, vsock and actual clock isolation remain
hardware tests, not conclusions from a successful cross-build.

The boot command line must include `epoch.run=RUN_ID`, `epoch.nonce=64_HEX_CHARS`
and `epoch.cid=3`, plus `init=/usr/lib/systemd/systemd` (adjust to the prepared
userspace's actual systemd location) and `systemd.unit=epoch.target`. The image
also installs that target as default.target. The target starts only epoch-agent;
it does not pull in the inherited basic/sysinit/multi-user target dependency
trees. Systemd remains PID 1 and provides process reaping and signal handling.
The image recipe masks time synchronizers, scheduled processing and network
targets offline. It rejects inherited drop-ins/dependencies that could customize
the controlled target or agent and masks inherited system/environment generators
before boot. It never calls host systemctl, chroot, mount or a clock setter.

Agent startup requires guest root, PID other than 1, valid boot markers and an
observed non-host vsock CID exactly 3. These prevent accidental host execution;
they are not attestation or protection against a malicious root operator.
It listens on guest vsock port 7000. No workload starts until an initial absolute
clock change has passed local readback validation. Live services require an
explicit allow_live flag on subsequent clock changes.

Workload commands use fixed argv from `/etc/epoch/workload.json`, a fixed guest
working directory, and UID/GID 10001. The agent launches its internal exec helper
with dropped credentials and no supplementary groups. A read-only `/dev/vsock`
descriptor lets this helper recheck CID without changing guest device permissions;
the descriptor is closed before workload exec. The helper verifies empty active
capability sets and sets PR_SET_NO_NEW_PRIVS on its locked exec thread. The agent
unit also sets NoNewPrivileges. Its bounding set contains only the capabilities
needed for guest clock control, credential drop and owned-process cleanup. It makes
only `CAP_SETUID` ambient so the root agent can enter UID 10001 on systemd versions
that do not otherwise retain it as effective; the kernel clears that capability
when the child changes UID, before the helper verifies that its permitted,
effective and ambient sets are empty. Workloads cannot gain clock-setting
capability through a setuid executable or file capabilities. This is a trusted
workload lab profile, not a general sandbox for hostile code.

## Wire protocol

The host sends `CONNECT 7000\n` to the per-run Unix socket. The bounded `OK N\n`
reply contains an assigned host port, which need not equal 7000. Buffered bytes
are preserved. Further frames are NDJSON, at most 1 MiB including newline.
Version `epoch-guest/v1`, request ID, operation, run ID and nonce are echoed in
every response. A response contains exactly one result or typed error.
Typed errors may retain bounded partial observations in details, including
clock samples and output-truncation flags. Partial data is never a successful
required observation for assertions.
Malformed frames, duplicate IDs and lost connections end that session. No action
or service request is automatically retried after a lost reply: outcome is unknown.

Operations are hello, clock.read, clock.set, action.exec, service.start,
service.stop and shutdown. Hello identifies the agent binary hash/build version
and embedded workload manifest hash. Control readiness does not imply the clock
barrier is open. Clock results contain actual realtime and monotonic samples.
Clock-set readback permits the local measured operation duration plus the recorded
tolerance; transport delay is excluded. Realtime then advances normally.

Action JSON input is at most 64 KiB; stdout/stderr each have a 64 KiB cap.
Required stdout must be valid JSON, including for a normal nonzero exit. Capture
overflow returns an execution error and drains/discards excess bytes during
termination. Deadlines use Go monotonic time. Services run in owned process
groups, use a declared readiness action, and are checked for unexpected death.
The supervisor observes leader exit with waitid WNOWAIT, retains that leader's
PID while terminating and checking its remaining process-group members, and only
then reaps the leader. Group signalling is serialized against reaping. A bounded
WaitDelay also prevents surviving output descriptors from blocking forever.
Readiness probes alone may be repeated within their deadline; declared business
actions are never replayed. Service diagnostics are returned on explicit stop.
Workloads must not daemonize or intentionally escape their owned process group.

## Preparing a workload image

First build local binaries with the pinned toolchain, without running the agent:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/epoch-agent ./cmd/epoch-agent
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/clock-probe ./workloads/clock-probe
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/agent-authority-expiry ./workloads/agent-authority-expiry
```

Before administration, inspect the userspace, kernel source identity,
binary identities and [preparation script](../scripts/build-guest-image.sh).
Directory inputs must contain a `.epoch-prepared-userspace` marker. Regular
squashfs inputs are inspected with unsquashfs and extracted only into a newly
created staging destination, with one worker, bounded decompressor queues, strict
errors, and no xattrs. Both forms must provide complete systemd dependencies,
empty dev/proc/sys/run directories, and no devices/sockets. A failing extracted
tree is retained for operator review; the helper does not silently remove entries.
The script rejects symlinks in every path it edits. A minimal prepared userspace
is required; the script replaces its account database with locked root and the
fixture account and does not preserve application accounts.

The following is an explicit operator action requiring administrator approval;
Epoch never invokes it. Run it from an approved administrator shell:

```sh
bash scripts/build-guest-image.sh --apply \
  /absolute/guest-inputs/ubuntu-24.04.squashfs \
  /absolute/guest-inputs/vmlinux-6.1.155 \
  /absolute/bin/epoch-agent /absolute/bin/clock-probe \
  /absolute/epoch/workloads/clock-probe/workload.json \
  epoch-clock-probe-v1 \
  /home/OPERATOR/epoch-lab/images/clock-probe-v1 \
  'firecracker-ci/v1.15/x86_64; candidate identities in artifacts/guest-inputs.candidate.lock.json' 2048
```

To prepare the authority-expiry reference instead, supply
`bin/agent-authority-expiry`, its adjacent `workload.json`, image ID
`epoch-agent-authority-expiry-v1`, and a different new output directory. The
legacy clock-probe-only positional form remains accepted for existing operator
procedures. The generic form validates that every declared command uses the one
workload binary installed at `/usr/local/libexec/WORKLOAD_ID`.

This candidate uses the explicit Ubuntu 24.04 squashfs and Linux 6.1.155 kernel
objects observed in the upstream v1.15 image listing. Firecracker itself remains
v1.16.1; no compatibility or boot success is inferred from the image listing.
Before approving preparation, compare the local input SHA-256 and byte sizes
with the candidate lock and inspect `unsquashfs -stat` and `unsquashfs -lls` output
on Rocky. These are read-only inspections. The supplied kernel configuration
records the required drivers built in, but that does not prove a working guest.
The helper also accepts an absolute marked userspace directory in place of the
squashfs argument. Rocky needs its local squashfs-tools package for extraction;
the helper never installs packages or invokes administrator elevation itself.

The isolated `demos/incident-response-agent` application has a demo-owned wrapper
around this generic recipe. It takes a marked userspace that already contains an
offline PostgreSQL package closure, adds only the demo processes and startup
ordering, and rebuilds the new image without mount or chroot. The generic recipe
still installs one declared workload binary and has no MCP, JWT, or PostgreSQL
knowledge. See the demo README for the explicit preparation commands and the
scope and provenance of its recorded working-tree hardware exercise.

The output parent must already exist, belong to the ordinary operator and have
mode 0700. The output directory must not exist. Preparation copies or extracts
userspace into a private retained staging directory, writes only inside it, and formats only a
new regular image file with mke2fs -d. Read-only e2fsck validates filesystem
structure. It does not mount the image or execute guest files. Staging remains
for inspection, including on failure; no broad recursive cleanup is performed.
The agent and kernel inputs must be local ELF x86_64 files; an ELF check does not
prove a kernel is bootable or compatible. Only default agent build version
0.1.0-dev is supported by this initial recipe; custom build versions require
matching metadata updates before use.

Output includes kernel.elf, rootfs.ext4, workload.json, preparation-inputs.json
and `IMAGE_ID.json`. Point artifact_manifest_dir at that output directory.
Preparation metadata records the local squashfs/kernel/binary hashes and sizes
before extraction. It verifies the copied kernel, installed binaries, and embedded
workload manifest against those identities. The image recipe records its
transformation and the preparation metadata digest. Directory inputs have an
explicitly labelled marker digest; no whole-directory input digest is claimed.
Hashes are labelled locally-observed. Record upstream published hashes and source
revisions separately when available; locally observed hashes are not signatures
or provenance proof. The metadata records local transformations. Bit-for-bit
reproducibility has not been measured. Real boot validation remains explicit
through the hardware test runner after an operator has reviewed the disposable
image and granted clock-change opt-in.
