#!/usr/bin/env bash
# Prepare a personally controlled, bare-metal Rocky Linux 9 x86_64 host.
# Administrative setup is separate from the future Go execution engine.
# Run as the intended ordinary account. Only --apply invokes sudo.
set -Eeuo pipefail
export LC_ALL=C
export PATH=/usr/sbin:/usr/bin:/sbin:/bin
umask 077

readonly RULE_DIR=/etc/udev/rules.d
readonly RULE="$RULE_DIR/99-epoch-kvm.rules"
readonly RULE_TEXT='SUBSYSTEM=="misc", KERNEL=="kvm", OWNER="root", GROUP="kvm", MODE="0660"'
readonly KVM_DEVICE=/dev/kvm
MODE=check
ASSUME_YES=false
OPERATOR=
KVM_GID=
MISSING=()

info() { printf '[INFO] %s\n' "$*"; }
pass() { printf '[PASS] %s\n' "$*"; }
warn() { printf '[WARN] %s\n' "$*" >&2; }
die() { printf '[FAIL] %s\n' "$*" >&2; exit 1; }
trap 'rc=$?; printf "[FAIL] Operation failed at line %s (command status %s). Stop and inspect; completed changes are retained.\n" "$LINENO" "$rc" >&2; exit 1' ERR

usage() {
    cat <<'HELP'
Usage: bash scripts/bootstrap-host.sh [--check | --apply | --probe] [--yes]

  --check  Read-only host/setup checks (default). No sudo, downloads, or VM.
  --apply  Install missing utilities and configure this account's KVM access.
           Uses sudo for the specific administrative operations only.
  --probe  Require a passing check, then create/release one empty KVM VM.
           No guest memory, vCPUs, guest code, or clock changes.
  --yes    Accept DNF's transaction prompt; valid only with --apply.
  --help   Show this help.

Run as an ordinary local account, not under sudo or a root shell.
Exit: 0 = selected operation passed; 1 = failed; 2 = usage error;
      3 = configuration present but a new login is required.

Scope: Rocky Linux 9, x86_64, bare metal, SELinux enforcing, cgroup v2.
No Firecracker/guest downloads, Go install, networking, time-service changes,
module loading, disk formatting, VM boot, or Kubernetes configuration.
HELP
}

parse_args() {
    local selected=false
    while (( $# )); do
        case "$1" in
            --check|--apply|--probe)
                if "$selected"; then usage >&2; exit 2; fi
                MODE="${1#--}"; selected=true ;;
            --yes) ASSUME_YES=true ;;
            --help|-h) usage; exit 0 ;;
            *) usage >&2; exit 2 ;;
        esac
        shift
    done
    if "$ASSUME_YES" && [[ "$MODE" != apply ]]; then
        printf '[FAIL] --yes requires --apply.\n' >&2
        exit 2
    fi
}

has_gid() { [[ " $1 " == *" $2 "* ]]; }

# Reject unsupported environments before any administration. These restrictions
# are the current bootstrap profile, not universal Firecracker requirements.
require_profile() {
    [[ "$EUID" -ne 0 ]] || die 'Run as the ordinary operator, without sudo.'
    [[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] ||
        die 'This bootstrap targets Linux x86_64.'
    [[ -r /etc/os-release ]] || die 'Cannot inspect /etc/os-release.'
    # /etc/os-release is a trusted, host-administered file, not downloaded input.
    # shellcheck disable=SC1091
    . /etc/os-release
    [[ "${ID:-}" == rocky && "${VERSION_ID:-}" == 9* ]] ||
        die 'This bootstrap supports Rocky Linux 9 only; do not force it on another OS.'
    [[ "${VERSION_ID%%.*}" == 9 ]] || die 'Expected Rocky major version 9.'
    OPERATOR="$(id -un)"
    awk -F: -v u="$OPERATOR" '$1==u {found=1} END {exit !found}' /etc/passwd ||
        die 'Operator must be a local account; directory-managed accounts need separate administration.'

    local tool virt virt_rc=0 cap key value rest count=0 group_entry
    for tool in stat getent grep awk rpm systemd-detect-virt getenforce \
                systemctl timedatectl id uname; do
        command -v "$tool" >/dev/null || die "Base host tool missing: $tool. Repair the OS baseline first."
    done
    virt="$(systemd-detect-virt 2>&1)" || virt_rc=$?
    [[ "$virt" == none && "$virt_rc" -eq 1 ]] ||
        die "Expected bare metal; detection returned '$virt' (status $virt_rc)."
    [[ "$(getenforce)" == Enforcing ]] ||
        die 'SELinux is not enforcing. Review the host policy; this script does not change it.'
    [[ "$(stat -fc %T /sys/fs/cgroup)" == cgroup2fs ]] ||
        die 'Expected cgroup v2. This script does not change boot parameters.'
    [[ -c "$KVM_DEVICE" && ! -L "$KVM_DEVICE" ]] ||
        die '/dev/kvm must already be a real character device. Inspect firmware/driver support.'
    group_entry="$(getent group kvm)" || die 'kvm group is absent; inspect the OS configuration.'
    IFS=: read -r _ _ KVM_GID _ <<< "$group_entry"
    [[ "$KVM_GID" =~ ^[0-9]+$ ]] || die 'Cannot determine the KVM group ID.'

    # Capabilities are thread/process attributes, not implied by the username.
    # Inspect the actual Bash process, not its child commands.
    while read -r key value rest; do
        case "$key" in
            CapPrm:|CapEff:|CapAmb:)
                [[ "$value" =~ ^[0-9a-fA-F]+$ ]] || die 'Cannot parse process capabilities.'
                cap=$((16#$value))
                (( (cap & (1 << 25)) == 0 )) || die "$key includes CAP_SYS_TIME; use an unprivileged session."
                count=$((count + 1)) ;;
        esac
    done < "/proc/$$/status"
    [[ "$count" -eq 3 ]] || die 'Incomplete process capability information.'
    pass "Profile: ${PRETTY_NAME:-Rocky Linux}; $(uname -r); operator=$OPERATOR"
    pass 'Bare-metal detection, SELinux enforcing, cgroup v2, and no CAP_SYS_TIME in inspected sets'
}

need_package() {
    local package="$1" existing
    for existing in "${MISSING[@]}"; do [[ "$existing" != "$package" ]] || return 0; done
    MISSING+=("$package")
}

collect_missing() {
    MISSING=()
    local cmd pkg
    # Check tools, not a preferred curl package name: curl-minimal is acceptable.
    while read -r cmd pkg; do
        command -v "$cmd" >/dev/null || need_package "$pkg"
    done <<'TOOLS'
curl curl-minimal
tar tar
gzip gzip
python3 python3
git git
sha256sum coreutils
timeout coreutils
mktemp coreutils
truncate coreutils
tee coreutils
script util-linux
unsquashfs squashfs-tools
mkfs.ext4 e2fsprogs
e2fsck e2fsprogs
getfacl acl
udevadm systemd-udev
usermod shadow-utils
restorecon policycoreutils
TOOLS
    rpm -q --quiet ca-certificates || need_package ca-certificates
}

rule_conflict_check() {
    [[ ! -L "$RULE_DIR" ]] || die 'Rule directory is a symlink; inspect it manually.'
    if [[ -e "$RULE_DIR" ]]; then
        [[ -d "$RULE_DIR" ]] || die 'Rule directory path is not a directory.'
        local mode
        mode="$(stat -c %a "$RULE_DIR")"
        [[ "$(stat -c %u "$RULE_DIR")" == 0 ]] && (( (8#$mode & 0022) == 0 )) ||
            die 'Rule directory must be root-owned and not group/other-writable.'
    fi
    [[ ! -L "$RULE" ]] || die "Refusing a symlink at $RULE."
    if [[ -e "$RULE" ]]; then
        [[ -f "$RULE" && -r "$RULE" ]] || die "Rule is not a readable regular file: $RULE"
        [[ "$(cat "$RULE")" == "$RULE_TEXT" ]] ||
            die "Existing $RULE differs. Review it; no overwrite will be attempted."
        [[ "$(stat -c '%u:%g:%a' "$RULE")" == 0:0:644 ]] ||
            die "Existing rule ownership/mode differs from root:root 0644: $RULE"
    fi
}

require_base_acl() {
    local acl
    acl="$(getfacl -cp -- "$KVM_DEVICE")" || die 'Cannot inspect the KVM access control list.'
    if grep -Eq '^(user|group):[^:]+:|^mask:|^default:' <<< "$acl"; then
        printf '%s\n' "$acl" >&2
        die 'KVM has an extended ACL. Review the existing access policy; no ACL will be removed.'
    fi
}

report_time_and_capacity() {
    info 'Host observations (no clock/timezone/service changes):'
    local status
    if status="$(timedatectl show -p Timezone -p NTP -p NTPSynchronized 2>/dev/null)"; then
        printf '%s\n' "$status"
        if ! grep -Fxq 'NTPSynchronized=yes' <<< "$status"; then
            warn 'Host synchronization is not confirmed. Review normal time service before temporal experiments.'
        fi
    else
        warn 'Cannot read timedatectl status; review the time service before temporal experiments.'
    fi
    if systemctl is-active --quiet chronyd; then
        pass 'chronyd is active (left unchanged)'
    else
        warn 'chronyd is not active; no service changes are made by this script.'
    fi
    free -h
    df -hT / "$HOME"
}

check_configuration() {
    local failures=0 pending=false configured active observed
    collect_missing
    if (( ${#MISSING[@]} )); then
        warn "Missing packages for required tools: ${MISSING[*]} (use --apply after review)."
        failures=$((failures + 1))
    else
        pass 'Required host utilities are available'
    fi
    if command -v python3 >/dev/null; then
        if python3 -c 'import sys; raise SystemExit(0 if sys.version_info >= (3,9) else 1)'; then
            pass 'Python >= 3.9 (bootstrap probe only; the Epoch engine is Go)'
        else
            warn 'Python >= 3.9 required; no interpreter replacement is attempted.'
            failures=$((failures + 1))
        fi
    fi
    if [[ -f "$RULE" ]]; then pass 'Persistent rule matches the declared policy';
    else warn "Persistent rule absent: $RULE"; failures=$((failures + 1)); fi
    observed="$(stat -c '%u:%g:%a' "$KVM_DEVICE")"
    if [[ "$observed" == "0:$KVM_GID:660" ]]; then
        pass 'Live device ownership/mode: root:kvm 0660'
    else
        warn "Live KVM state $observed; expected 0:$KVM_GID:660."
        failures=$((failures + 1))
    fi
    if command -v getfacl >/dev/null; then require_base_acl; pass 'No extended KVM ACL observed'; fi
    configured="$(id -G "$OPERATOR")"
    active="$(id -G)"
    if ! has_gid "$configured" "$KVM_GID"; then
        warn "Account $OPERATOR is not configured in kvm."
        failures=$((failures + 1))
    elif ! has_gid "$active" "$KVM_GID"; then
        pending=true
        warn 'Account membership is configured, but this session needs a new login.'
    elif [[ -r "$KVM_DEVICE" && -w "$KVM_DEVICE" ]]; then
        pass 'Current ordinary session has KVM group membership and read/write permission'
    else
        warn 'Current session cannot read/write KVM; investigate policy rather than using sudo.'
        failures=$((failures + 1))
    fi
    if (( failures )); then return 1; fi
    if "$pending"; then return 3; fi
    return 0
}

apply_configuration() {
    command -v sudo >/dev/null || die 'sudo is required for --apply.'
    command -v dnf >/dev/null || die 'dnf is required for --apply.'
    info "Administrative target: current account $OPERATOR; $RULE; $KVM_DEVICE"
    info 'No service, network, bootloader, host-clock, Go, or Firecracker configuration is performed.'
    if command -v getfacl >/dev/null; then require_base_acl; fi
    collect_missing
    if (( ${#MISSING[@]} )); then
        info "Install missing utility packages: ${MISSING[*]}"
        # Install only missing utility packages; retain DNF's normal review prompt.
        # Dependency resolution may also update related packages: inspect the transaction.
        if "$ASSUME_YES"; then sudo dnf install -y -- "${MISSING[@]}";
        else sudo dnf install -- "${MISSING[@]}"; fi
    else
        pass 'No utility packages need installation'
    fi
    collect_missing
    (( ${#MISSING[@]} == 0 )) || die "Required utilities still absent: ${MISSING[*]}"
    require_base_acl

    if has_gid "$(id -G "$OPERATOR")" "$KVM_GID"; then
        pass 'Account already belongs to kvm; existing groups preserved'
    else
        info "Append kvm membership for $OPERATOR"
        sudo usermod -aG kvm -- "$OPERATOR"
    fi

    local reload=false
    if [[ ! -e "$RULE" ]]; then
        info "Create $RULE without overwriting another file"
        if [[ ! -d "$RULE_DIR" ]]; then
            sudo install -d -o root -g root -m 0755 -- "$RULE_DIR"
        fi
        # Assemble in the trusted directory, then publish with a non-clobbering
        # hard link. No privileged code is loaded from a user-writable temp file.
        sudo /bin/bash -s -- "$RULE" "$RULE_TEXT" <<'ROOT'
set -euo pipefail
umask 022
dest=$1
text=$2
tmp=$(mktemp /etc/udev/rules.d/.epoch-kvm.XXXXXX)
trap 'rm -f -- "$tmp"' EXIT
printf '%s\n' "$text" > "$tmp"
chmod 0644 "$tmp"
ln -T -- "$tmp" "$dest"
ROOT
        sudo restorecon -- "$RULE"
        reload=true
    else
        pass 'Matching existing rule retained'
    fi
    if [[ "$(stat -c '%u:%g:%a' "$KVM_DEVICE")" != "0:$KVM_GID:660" ]]; then reload=true; fi
    if "$reload"; then
        info 'Reload udev and trigger only the KVM device'
        sudo udevadm control --reload-rules
        sudo udevadm trigger --action=change --subsystem-match=misc --sysname-match=kvm
        sudo udevadm settle --timeout=10
    else
        pass 'Live KVM permissions already match; no device event requested'
    fi
    rule_conflict_check
}

probe_kvm() {
    info 'Active probe: create and release one EMPTY VM as the ordinary account.'
    local rc=0
    timeout --kill-after=2s 10s python3 -u - <<'PY' || rc=$?
import fcntl
import os
import sys
from contextlib import ExitStack

if os.geteuid() == 0:
    raise SystemExit("FAIL: the probe must not run as root")
try:
    with ExitStack() as cleanup:
        kvm = os.open("/dev/kvm", os.O_RDWR | os.O_CLOEXEC)
        cleanup.callback(os.close, kvm)
        version = fcntl.ioctl(kvm, 0xAE00, 0)  # KVM_GET_API_VERSION on x86_64.
        if version != 12:
            raise RuntimeError(f"expected KVM API 12, observed {version}")
        vm = fcntl.ioctl(kvm, 0xAE01, 0)       # KVM_CREATE_VM, type 0.
        cleanup.callback(os.close, vm)
        print("PASS: KVM API 12; empty VM created without guest memory or vCPUs")
    print("PASS: VM and KVM descriptors closed")
except (OSError, RuntimeError) as exc:
    print(f"FAIL: {exc}", file=sys.stderr)
    raise SystemExit(1)
PY
    [[ "$rc" -eq 0 ]] || die "KVM probe failed (command status $rc); no guest boot has been proven."
    pass 'Empty-VM probe passed; Firecracker boot and temporal isolation remain separate tests'
}

main() {
    parse_args "$@"
    require_profile
    rule_conflict_check
    report_time_and_capacity
    if [[ "$MODE" == apply ]]; then apply_configuration; fi
    local rc=0
    check_configuration || rc=$?
    case "$rc" in
        0) pass 'Host prerequisite policy checks passed' ;;
        3) warn 'Keep this connection open; start a new SSH session, then run --probe.'; exit 3 ;;
        *) die 'Prerequisite checks failed. Inspect the findings before proceeding.' ;;
    esac
    if [[ "$MODE" == probe ]]; then probe_kvm;
    else info 'No VM created. Use --probe for the separate functional KVM test.'; fi
}

# Guard allows focused shell-function tests without invoking administration.
if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    # Do not wrap main in an `if`/`||`: that would suppress Bash errexit inside it.
    # Expected pending-login exit is handled explicitly, preserving failure stops.
    main "$@"
fi