# Bare-metal host prerequisites

Prepare a personally controlled Rocky Linux host for Epoch's local microVM experiments. The goal is explicit, verifiable host access—not a running application or a production security certification.

Epoch is an application-independent temporal-testing platform for stateful Linux applications. Its execution engine is being built in Go. Subscription/batch-expiry and non-AI knowledge-search services are separate reference workloads. This bootstrap prepares the host; it does not implement the engine. Kubernetes integration is outside this initial path.

**Host clock invariant:** Epoch must not set, step, or simulate the host clock. Temporal changes belong only inside a guest. Normal host time synchronisation stays separate and enabled according to the host's existing policy.

## 1. Scope and acceptance gate

This procedure starts **after Linux installation and working SSH access**. Run commands in a Bash session on the Rocky host—not in an unconnected Windows Git Bash prompt.

| Area | Bootstrap profile |
| --- | --- |
| Operating system | Rocky Linux 9.x, x86_64, systemd; ordinary local account |
| Virtualisation | Bare-metal detection; firmware virtualisation and the host KVM driver already provide `/dev/kvm` |
| Security baseline | SELinux enforcing; cgroup v2; no `CAP_SYS_TIME` in the bootstrap process's permitted, effective, or ambient sets |
| Access policy | Existing `kvm` group; operator membership; `/dev/kvm` owned by `root:kvm`, mode `0660`, with no extended ACL |
| Administration | The operator can approve narrowly scoped `sudo` operations |
| Connectivity | Existing SSH access; trusted package repositories reachable only when missing utilities need installation |
| Workloads | Trusted local experiments, not arbitrary public or hostile workloads |

These are **this bootstrap's boundaries**, not a claim that Firecracker requires this exact distribution, access mechanism, or hardware size. Firecracker requires usable KVM read/write access; upstream documents group- and ACL-based arrangements. This script deliberately implements the group-based profile and refuses to replace an ACL-managed policy. [1]

Completion means the policy checks pass and the ordinary operator can create and release one empty KVM VM. It does **not** establish Firecracker boot compatibility, guest-time isolation, safe public multi-tenancy, or application correctness.

## 2. Repository files and quick start

```text
scripts/bootstrap-host.sh    # Explicit host administration and checks
docs/prerequisites.md       # This guide
```

Run from the repository root. Read the script before using apply mode:

```bash
less scripts/bootstrap-host.sh
bash -n scripts/bootstrap-host.sh
bash scripts/bootstrap-host.sh --check
```

A missing package or missing KVM policy is an expected finding on an unprepared host. Unsupported environments, root execution, conflicting rules, and ACL-managed access are stop conditions—not permission to force the script through.

After reviewing the findings, apply the declared setup:

```bash
bash scripts/bootstrap-host.sh --apply
```

**Do not put `sudo` before the whole script.** It selects the calling ordinary account and elevates only specific administrative commands. By default, DNF asks you to review the package transaction. `--apply --yes` accepts that package prompt; it does not bypass any safety check.

If the script reports exit `3`, keep the original connection open, establish a **new SSH connection**, and run from the same checkout:

```bash
id
bash scripts/bootstrap-host.sh --probe
```

When membership was already active, the new connection is unnecessary. Use `--probe` once checks pass. Capture a command's status immediately with `echo $?`, before running another command.

### Command contract

| Command | Side effects | Successful meaning |
| --- | --- | --- |
| `--check` (default) | No intentional host-state changes, sudo, downloads, or VM creation | Current host prerequisites match the declared profile |
| `--apply` | Missing utility-package installation; operator group membership; one local rule and targeted device event if needed | Applied state passes checks, or a new-login requirement is explicitly reported |
| `--probe` | After checks pass, create and release one empty KVM VM under the current ordinary identity | Basic KVM operation and descriptor cleanup succeeded |

| Exit code | Meaning | Action |
| --- | --- | --- |
| `0` | Selected operation passed | Proceed only as far as that operation proves |
| `1` | Prerequisite or operation failed | Inspect output; preserve the last successful checkpoint |
| `2` | Invalid arguments | Read `--help` |
| `3` | Account configured, but current session lacks the new group membership | Start a new login and repeat `--probe` |

Time-service warnings are advisory and do not themselves change the exit code. Investigate unresolved warnings before performing temporal experiments. A prerequisite pass is not evidence that clock isolation has been tested.

## 3. What apply mode changes

### Utility packages

The script first checks for the tools it actually needs. It installs missing utility packages from the host's configured repositories; it does not run a general system upgrade or add repositories. DNF dependency resolution can still update related packages or execute package scripts. Review the proposed transaction. This is not a bit-for-bit reproducible OS image. [2]

| Tools | Package family | Purpose |
| --- | --- | --- |
| `curl`, CA trust | Existing curl provider, or `curl-minimal`; `ca-certificates` | HTTPS artifact retrieval in later steps |
| `tar`, `gzip`, `sha256sum` | `tar`, `gzip`, `coreutils` | Verify and unpack archives; detect missing extractors before downloading |
| `python3` | `python3` | Standard-library-only KVM bootstrap probe; **not** the Go engine |
| `git` | `git` | Source checkout and review |
| `script`, core file utilities | `util-linux`, `coreutils` | Later console capture and local-file preparation |
| `unsquashfs` | `squashfs-tools` | Later guest filesystem preparation |
| `mkfs.ext4`, `e2fsck` | `e2fsprogs` | Later filesystem-image creation and checks |
| `getfacl` | `acl` | Detect access policies beyond mode bits |
| `udevadm`, `usermod`, `restorecon` | `systemd-udev`, `shadow-utils`, `policycoreutils` | Apply the narrowly scoped access configuration |

A working `curl` supplied by `curl-minimal` is accepted without replacement. No pip packages are needed. Installing filesystem utilities does **not** format a disk: the bootstrap never invokes `mkfs.ext4`.

Core host inspection utilities, `/dev/kvm`, and the existing `kvm` group must already exist. The script stops rather than loading kernel modules, editing firmware settings, or pretending a permissions change can create virtualisation support.

### Operator membership

Apply mode appends `kvm` only when missing:

```bash
sudo usermod -aG kvm "$(id -un)"
```

`-aG` preserves other supplementary memberships. The new configuration does not update the groups of an already-running SSH shell; the script distinguishes account configuration from the current process's membership. [3]

### Persistent device policy

The only persistent configuration file it creates is:

```text
/etc/udev/rules.d/99-epoch-kvm.rules
```

Its content is:

```udev
SUBSYSTEM=="misc", KERNEL=="kvm", OWNER="root", GROUP="kvm", MODE="0660"
```

`==` matches the KVM device; `=` assigns its ownership and mode. Udev reads local rules from `/etc/udev/rules.d`. Creating or reloading a rule and applying it to an existing device are distinct operations. [4][5]

For a new file, the script assembles the rule in the root-owned rules directory and publishes it without overwriting another path. It restores the new file's normal SELinux label. [14] Existing matching `root:root 0644` rules are retained. Different content, unexpected ownership, a symlink, or an unsafe rules directory cause a stop.

When required, it reloads the rule configuration and triggers **only** the KVM device, then checks the resulting state. If both the rule and live permissions already match, it does not request another event. It never runs a global device retrigger.

Existing ACLs are inspected, not erased. POSIX named-user/group ACL entries can grant access beyond what a casual reading of owner/group/other bits suggests. This bootstrap refuses extended KVM ACLs rather than silently replacing an administrator's policy. [6]

### Explicit non-changes

The script does not configure SSH, firewall rules, TAP interfaces, forwarding, DNS, timezones, time services, the bootloader, firmware, kernel modules, a Go toolchain, Firecracker, guest images, or Kubernetes. It does not disable SELinux, mount or format storage, create a service, or reboot the host. Package-manager transactions remain privileged operations with their normal package-maintainer side effects.

KVM access is a meaningful host privilege; grant it only to trusted accounts. Mode changes do not revoke device descriptors already opened by other processes. The live permission check is not a complete access audit or a reboot-persistence test.

## 4. Why the functional probe is separate

File permissions answer whether access appears allowed. `--probe` tests an actual operation:

```text
Open /dev/kvm → require KVM API 12 → create empty VM → close VM → close KVM
```

The kernel's API specifies that a newly created VM has no configured guest memory or vCPUs. The probe configures neither and executes no guest code. The VM's lifetime is associated with its file-descriptor references. [7]

The embedded Python probe registers cleanup immediately after acquiring each descriptor. `ExitStack` releases registered resources in reverse order, including on exceptions. A timeout requests termination after 10 seconds, with escalation after another 2 seconds; this is not a hard guarantee against an unresponsive kernel. [8][9]

Expected probe output includes:

```text
PASS: KVM API 12; empty VM created without guest memory or vCPUs
PASS: VM and KVM descriptors closed
```

The process-capability check is point-in-time evidence. Absence of `CAP_SYS_TIME` from this Bash process does not prove the privileges of every future program, eliminate the operator's administrative authority, or implement a sandbox. The future Go runner needs its own enforced boundary. [10]

## 5. Keep runtime artifacts separate

Host setup ends before downloads or VM startup. Use a user-owned artifact area outside the source tree, for example:

```text
$HOME/epoch-lab/
├── tools/       # Pinned Firecracker releases
├── artifacts/   # Guest kernels and input filesystems
└── runs/        # Private per-experiment state and evidence
```

No cloud runtime is required. Initial package and artifact downloads may use external HTTPS services; this is not an air-gapped bootstrap. Do not commit binaries, guest disks, memory snapshots, credentials, or machine-specific logs with this change.

### Existing Firecracker checkpoint

The development sequence selected **Firecracker v1.16.1, x86_64**. Its release archive has the published SHA-256 below. The version is explicit, not a command to track the newest release automatically. [11]

```text
Artifact: firecracker-v1.16.1-x86_64.tgz
SHA-256: 382a02a869e4d6d5cb14c40577f9545e8458021ea8b0b2d3fc10ec14d9c242e6
```

On a host using the documented existing layout, these commands check that checkpoint without re-downloading or extracting anything:

```bash
(
  set -euo pipefail
  cd "$HOME/epoch-lab/tools/firecracker-v1.16.1-x86_64"
  printf '%s  %s\n' \
    '382a02a869e4d6d5cb14c40577f9545e8458021ea8b0b2d3fc10ec14d9c242e6' \
    'firecracker-v1.16.1-x86_64.tgz' | sha256sum --check -
  timeout --kill-after=2s 10s \
    ./release-v1.16.1-x86_64/firecracker-v1.16.1-x86_64 --version
)
```

This is optional verification of a **separately prepared artifact**, not a requirement for `--check` or `--probe`. A new host can finish this bootstrap before downloading Firecracker. Then use the official release assets, verify the archive **before extraction**, and extract to a new staging directory. Do not pipe an unverified download into an extractor or execute it as root.

Guest-kernel and root-filesystem selection is another separate step. A Firecracker release tag does not prove that a similarly named CI-image prefix exists. Inspect published objects and record the selected keys and their hashes; do not guess missing filenames or select mutable “latest” artifacts on each test run.

The Rocky 5.14 host below is outside the pinned release's listed host validation matrix. Upstream compatibility policy, an executable's version output, an empty-VM probe, and a successful guest boot are different evidence. Test the recorded combination without claiming upstream certification. [12]

## 6. Reference observations and validation boundaries

Manual development observations on September 6, 2026:

| Observation | Recorded result |
| --- | --- |
| OS and kernel | Rocky Linux 9.8; `5.14.0-687.42.1.el9_8.x86_64` |
| Hardware | Bare-metal Intel i7-1265U; 12 logical CPUs; approximately 30 GiB RAM visible |
| Storage | XFS; separate home filesystem approximately 844 GiB; not a minimum requirement |
| Security | SELinux enforcing; cgroup v2 |
| KVM | `root:kvm 0660`; ordinary-user empty-VM creation and cleanup passed |
| Host time | Existing normal synchronisation active and reported synchronised |
| Firecracker | Published archive digest matched; `Firecracker v1.16.1` version command exited successfully |
| Not established | Guest boot, guest-time isolation, workload correctness, production hardening, or reboot persistence |

Those results came from the earlier **manual commands**, not from an end-to-end run of this consolidated script on Rocky. The consolidated script must still be run and checked on the target host. Its 30 authoring checks use isolated fixtures and syntax validation; they do not replace real DNF, udev, SELinux, or KVM testing. ShellCheck was not available in the authoring environment.

### Retain evidence without committing machine details

Save the command, source commit, complete output, and exit code when validating a host. To preserve the script's exit code while also recording output:

```bash
mkdir -p "$HOME/epoch-lab/evidence"
(
  set -o pipefail
  bash scripts/bootstrap-host.sh --probe 2>&1 |
    tee "$HOME/epoch-lab/evidence/host-probe.log"
)
probe_rc=$?
printf 'Probe exit code: %s\n' "$probe_rc"
```

Review logs before publication. Usernames, filesystem layouts, package sources, and local paths may identify the environment. Retain DNF transaction details when packages change. Re-run the check and probe after a **planned** reboot before claiming that persistence has been observed.

## 7. Troubleshooting and recovery

| Finding | Meaning and next action |
| --- | --- |
| Script refuses root | Run it as the intended ordinary SSH account; use `--apply` for explicit administration |
| Wrong OS/architecture or detected virtualisation | Outside this bootstrap profile; adapt and review a separate procedure rather than weakening the guard |
| `/dev/kvm` absent | Investigate firmware virtualisation and the host KVM driver; a chmod or group change cannot resolve this |
| Missing `tar`, `gzip`, or image utilities | Apply missing packages, then resume the artifact step that failed; do not discard a verified archive |
| Matching udev rule already exists | Accepted as existing state; not a reason to delete it or recreate the installation |
| Different rule or unexpected ACL | Inspect the existing administrator/desktop access policy; no automatic overwrite or ACL removal |
| Exit `3` after apply | Group configured but the shell is stale; open a new SSH connection and repeat the probe |
| Permission denied after a fresh login | Check `id`, device state, ACLs, and relevant SELinux denials; do not disable SELinux or switch the probe to root |
| NTP/chronyd warning | Investigate normal host synchronisation separately; this script deliberately does not repair it |
| Package installation or udev operation fails | Read the original failure; prior changes may already have succeeded; inspect and rerun once the cause is addressed |
| Probe times out | Inspect kernel/host diagnostics; do not classify it as a successful boot or retry indefinitely |

Apply mode is repeatable but **not transactional**: package installation, membership, and device-rule application have different commit points. A failure does not undo earlier successful operations. Preserve the output and resume by rerunning after resolving the cause. Conflicting state is never treated as already complete.

Run only one bootstrap administration session at a time. Do not assume that editing permissions is safe on a shared machine with active users or workloads; that deployment model is outside this guide.

## 8. Reversal and ownership

There is no blanket uninstall command. The script may adopt a matching rule or group membership that predates this run, so automatic deletion would be unsafe. Inspect the before-state and remove only changes you own.

If **this setup created the rule**, and you have reviewed the effect of returning to the distribution's existing policy:

```bash
sudo cat /etc/udev/rules.d/99-epoch-kvm.rules
# Only after confirming ownership of this change:
sudo rm -- /etc/udev/rules.d/99-epoch-kvm.rules
sudo udevadm control --reload-rules
sudo udevadm trigger --action=change --subsystem-match=misc --sysname-match=kvm
sudo udevadm settle --timeout=10
ls -l /dev/kvm
getfacl -p /dev/kvm
```

Removing the rule restores rule selection, **not necessarily the original exact mode**; inspect the resulting access policy. The distribution may return to broader permissions. Do not remove another administrator's rule.

If this setup added the caller's `kvm` membership and no other work depends on it:

```bash
sudo gpasswd -d "$(id -un)" kvm
```

Use a new login to verify the account's new groups. Existing sessions and open KVM descriptors may retain access; a group edit is not an immediate revocation mechanism. Do not delete the shared `kvm` group.

Review package transactions individually. Do not blindly remove shared tools or undo a DNF transaction that other software now depends on. User-owned artifacts are separate from host configuration and are not deleted by this procedure.

## 9. Next boundary: a real guest, then the Go engine

After host preparation, separately verify the Firecracker executable, select explicit guest artifacts, and boot one private guest. Only then implement the Go lifecycle: validation, readiness, guest-only time control, deadlines, application steps, results, and owned cleanup. Both reference workloads should use that same execution path.

Production usage requires substantially more than group permissions: Firecracker's guidance covers jailer-equivalent process constraints, resource controls, patched kernels/microcode, and safe artifact/log handling. This bootstrap does not implement those controls or claim readiness for hostile workloads. [13]

## References

1. [Firecracker v1.16.1 — Getting started and KVM access](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/docs/getting-started.md)
2. [DNF — Command reference](https://dnf.readthedocs.io/en/latest/command_ref.html)
3. [shadow-utils — usermod manual](https://man7.org/linux/man-pages/man8/usermod.8.html)
4. [systemd — udev rule syntax and local administration](https://man7.org/linux/man-pages/man7/udev.7.html)
5. [systemd — udevadm reload and trigger](https://man7.org/linux/man-pages/man8/udevadm.8.html)
6. [Linux ACL utilities — getfacl](https://man7.org/linux/man-pages/man1/getfacl.1.html)
7. [Linux kernel — KVM API](https://docs.kernel.org/virt/kvm/api.html)
8. [Python 3.9 — contextlib.ExitStack](https://docs.python.org/3.9/library/contextlib.html#contextlib.ExitStack)
9. [GNU Coreutils — timeout](https://www.gnu.org/software/coreutils/manual/html_node/timeout-invocation.html)
10. [Linux — capabilities](https://man7.org/linux/man-pages/man7/capabilities.7.html)
11. [Firecracker v1.16.1 — published release assets and digests](https://github.com/firecracker-microvm/firecracker/releases/expanded_assets/v1.16.1)
12. [Firecracker v1.16.1 — kernel policy](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/docs/kernel-policy.md)
13. [Firecracker v1.16.1 — production host setup](https://github.com/firecracker-microvm/firecracker/blob/v1.16.1/docs/prod-host-setup.md)

14. [SELinux — restorecon](https://man7.org/linux/man-pages/man8/restorecon.8.html)