#!/usr/bin/env bash
# Offline preparation only. This script never boots a VM or mounts a filesystem.
set -euo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: build-guest-image.sh --apply PREPARED_USERSPACE KERNEL AGENT WORKLOAD_BINARY WORKLOAD_MANIFEST IMAGE_ID OUTPUT_DIR SOURCE_IDENTITY [SIZE_MIB]

Legacy clock-probe form remains accepted:
  build-guest-image.sh --apply PREPARED_USERSPACE KERNEL AGENT CLOCK_PROBE OUTPUT_DIR SOURCE_IDENTITY [SIZE_MIB]

Requires Linux, explicit administrator execution, mke2fs, e2fsck, jq, readelf,
coreutils and a complete local x86_64 systemd userspace. A local squashfs input
also requires unsquashfs. Nothing is downloaded.
OUTPUT_DIR must be new, absolute, and beneath an existing non-root-owned private
operator directory. PREPARED_USERSPACE is either a regular squashfs file or a
directory containing .epoch-prepared-userspace. Extraction stays in new staging.
Only a new regular image file is formatted. No mount or chroot is performed.
Staging is retained in OUTPUT_DIR for inspection, including after a failed build.
USAGE
}
fail() { printf '%s\n' "error: $*" >&2; exit 1; }
[[ ${1:-} == --apply ]] || { usage; exit 2; }
if [[ $# -ge 9 && $# -le 10 ]]; then
  manifest_argument=$6
  image_id=$7
  output=$8
  source_identity=$9
  size_mib=${10:-2048}
elif [[ $# -ge 7 && $# -le 8 ]]; then
  manifest_argument=$(dirname -- "${BASH_SOURCE[0]}")/../workloads/clock-probe/workload.json
  image_id=epoch-clock-probe-v1
  output=$6
  source_identity=$7
  size_mib=${8:-2048}
else
  usage
  exit 2
fi
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Linux x86_64 is required'
[[ $EUID -eq 0 ]] || fail 'explicit administrator execution is required; the script does not invoke sudo'
for command in realpath stat install cp mkdir mktemp truncate mke2fs e2fsck jq sha256sum readelf find; do
  command -v "$command" >/dev/null || fail "missing preparation tool: $command"
done
recipe_dir=$(realpath -e -- "$(dirname -- "${BASH_SOURCE[0]}")/../build/guest")

[[ ! -L $2 ]] || fail 'userspace input must not be a symlink'
userspace=$(realpath -e -- "$2")
kernel=$(realpath -e -- "$3")
agent=$(realpath -e -- "$4")
workload=$(realpath -e -- "$5")
[[ ! -L $manifest_argument ]] || fail 'workload manifest must not be a symlink'
manifest_source=$(realpath -e -- "$manifest_argument")
[[ $size_mib =~ ^[0-9]+$ && ${#size_mib} -le 4 ]] || fail 'invalid image size'
(( size_mib >= 256 && size_mib <= 4096 )) || fail 'image size must be 256..4096 MiB'
[[ -n $source_identity && ${#source_identity} -le 1024 ]] || fail 'source identity must contain 1..1024 characters'
userspace_hash=
userspace_size=0
userspace_marker_hash=
if [[ -f $userspace ]]; then
  userspace_kind=squashfs
  command -v unsquashfs >/dev/null || fail 'local squashfs input requires unsquashfs'
  userspace_size=$(stat -c %s -- "$userspace")
  (( userspace_size > 0 && userspace_size <= 4294967296 )) || fail 'squashfs input size must be between 1 byte and 4 GiB'
  unsquashfs -stat "$userspace" >/dev/null || fail 'input is not a readable squashfs filesystem'
  userspace_hash=$(sha256sum -- "$userspace"); userspace_hash=${userspace_hash%% *}
elif [[ $userspace != / && -d $userspace && -f $userspace/.epoch-prepared-userspace && ! -L $userspace/.epoch-prepared-userspace ]]; then
  userspace_kind=prepared-directory
  userspace_marker_hash=$(sha256sum -- "$userspace/.epoch-prepared-userspace"); userspace_marker_hash=${userspace_marker_hash%% *}
else
  fail 'userspace must be a regular squashfs file or a marked prepared directory'
fi
for file in "$kernel" "$agent" "$workload"; do
  [[ -f $file && ! -L $file ]] || fail "not a regular input file: $file"
  readelf -h "$file" | grep -q 'Advanced Micro Devices X86-64' || fail "input is not an x86_64 ELF file: $file"
done
[[ -f $manifest_source && ! -L $manifest_source ]] || fail 'workload manifest must be a regular file'
workload_id=$(jq -er '.id | select(type == "string" and test("^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$"))' "$manifest_source") || fail 'invalid workload ID'
workload_version=$(jq -er '.version | select(type == "string" and length > 0)' "$manifest_source") || fail 'invalid workload version'
jq -e --arg executable "/usr/local/libexec/$workload_id" -f "$recipe_dir/workload-image-contract.jq" "$manifest_source" >/dev/null || fail 'workload manifest identity, account, working directory or executable does not match the image recipe'
[[ $image_id =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$ ]] || fail 'invalid image ID'
[[ $output == /* && ! -e $output && ! -L $output ]] || fail 'output must be a new absolute path'
parent=$(realpath -e -- "$(dirname -- "$output")")
[[ $output == "$parent/$(basename -- "$output")" ]] || fail 'output path must be canonical without symlink ancestors or traversal'
[[ $(basename -- "$output") =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$ ]] || fail 'unsafe output directory name'
owner=$(stat -c %u -- "$parent")
group=$(stat -c %g -- "$parent")
[[ $owner -ne 0 ]] || fail 'output parent must belong to the ordinary operator'
[[ $(stat -c %a -- "$parent") == 700 ]] || fail 'output parent must have mode 0700'
validate_userspace_tree() {
  local root=$1 pseudo
  [[ -d $root && ! -L $root ]] || fail 'userspace root must be a real directory'
  [[ -z $(find "$root" -xdev \( -type b -o -type c -o -type s \) -print -quit) ]] || fail 'userspace contains devices or sockets; retained staging requires operator review'
  for pseudo in dev proc sys run; do
    [[ ! -L $root/$pseudo ]] || fail "symlink pseudo-filesystem path: $pseudo"
    [[ ! -d $root/$pseudo || -z $(find "$root/$pseudo" -mindepth 1 -print -quit) ]] || fail "userspace $pseudo must be empty; retained staging requires operator review"
  done
}
if [[ $userspace_kind == prepared-directory ]]; then validate_userspace_tree "$userspace"; fi

mkdir -m 0700 -- "$output"
trap 'printf "Preparation stopped; inspect retained staging at %s\n" "$output" >&2' ERR
stage=$(mktemp -d "$output/.staging.XXXXXXXX")
tree=$stage/root
[[ ! -e $tree && ! -L $tree && $(realpath -e -- "$stage") == "$stage" ]] || fail 'new extraction destination identity check failed'
kernel_input_hash=$(sha256sum -- "$kernel"); kernel_input_hash=${kernel_input_hash%% *}
agent_input_hash=$(sha256sum -- "$agent"); agent_input_hash=${agent_input_hash%% *}
workload_input_hash=$(sha256sum -- "$workload"); workload_input_hash=${workload_input_hash%% *}
jq --arg source "$source_identity" --arg kind "$userspace_kind" \
  --arg path "$userspace" --arg hash "$userspace_hash" \
  --arg marker "$userspace_marker_hash" --argjson size "$userspace_size" \
  --arg kp "$kernel" --arg kh "$kernel_input_hash" --argjson ks "$(stat -c %s -- "$kernel")" \
  --arg ap "$agent" --arg ah "$agent_input_hash" --argjson agent_size "$(stat -c %s -- "$agent")" \
  --arg wp "$workload" --arg wh "$workload_input_hash" --argjson ws "$(stat -c %s -- "$workload")" \
  -n -f "$recipe_dir/preparation-inputs.jq" \
  > "$output/preparation-inputs.json"
if [[ $userspace_kind == squashfs ]]; then
  unsquashfs -strict-errors -no-progress -processors 1 -data-queue 16 -frag-queue 16 -no-xattrs -dest "$tree" "$userspace"
else
  mkdir -m 0700 -- "$tree"
  cp -a -- "$userspace/." "$tree/"
fi
chmod 0700 -- "$stage"
[[ $(realpath -e -- "$tree") == "$stage/root" && ! -L $tree ]] || fail 'extracted userspace root escaped staging'
validate_userspace_tree "$tree"

# Never follow image-provided symlinks while editing the staged tree.
safe_target() {
  local relative=$1 part current=$tree
  [[ $relative != /* && $relative != *..* ]] || fail 'unsafe staged relative path'
  IFS=/ read -ra parts <<< "$relative"
  for part in "${parts[@]}"; do
    current=$current/$part
    [[ ! -L $current ]] || fail "image-provided symlink blocks staging edit: $relative"
  done
}
for directory in etc etc/epoch etc/systemd etc/systemd/system usr usr/local usr/local/libexec var var/lib var/lib/epoch-workload; do
  safe_target "$directory"
  mkdir -p -- "$tree/$directory"
  chown 0:0 -- "$tree/$directory"
  chmod 0755 -- "$tree/$directory"
done
chown 0:0 -- "$tree"
chmod 0755 -- "$tree"

# Inherited additions must not extend or override the controlled boot target.
for units in etc/systemd/system.control etc/systemd/system.attached etc/systemd/system usr/local/lib/systemd/system usr/lib/systemd/system lib/systemd/system; do
  [[ -d $tree/$units ]] || continue
  [[ $(realpath -e -- "$tree/$units") == "$tree/"* ]] || fail "unit search directory escapes staging: $units"
  for name in epoch.target epoch-agent.service default.target epoch-.service service target; do
    for suffix in d wants requires upholds; do
      [[ ! -e $tree/$units/$name.$suffix && ! -L $tree/$units/$name.$suffix ]] || fail "inherited unit customization requires review: $units/$name.$suffix"
    done
  done
done
# Generators execute before a target. Mask each inherited generator offline so
# prepared userspace cannot inject services or environment before agent readiness.
for kind in system-generators system-environment-generators; do
  safe_target "etc/systemd/$kind"
  mkdir -p -- "$tree/etc/systemd/$kind"
  for location in usr/lib/systemd usr/local/lib/systemd lib/systemd etc/systemd; do
    [[ -d $tree/$location/$kind ]] || continue
    [[ $(realpath -e -- "$tree/$location/$kind") == "$tree/"* ]] || fail "generator search directory escapes staging: $location/$kind"
    [[ ! -L $tree/$location/$kind ]] || fail "symlink generator directory requires review: $location/$kind"
    while IFS= read -r -d '' generator; do
      name=$(basename -- "$generator")
      [[ $name =~ ^[a-zA-Z0-9_.-]+$ && ! -d $generator ]] || fail 'unexpected generator entry'
      ln -sfnT /dev/null "$tree/etc/systemd/$kind/$name"
    done < <(find "$tree/$location/$kind" -mindepth 1 -maxdepth 1 -print0)
  done
done
for file in etc/passwd etc/group etc/shadow etc/gshadow etc/fstab etc/systemd/system/epoch.target etc/systemd/system/epoch-agent.service etc/epoch/workload.json usr/local/libexec/epoch-agent "usr/local/libexec/$workload_id"; do
  safe_target "$file"
done
[[ -f $tree/usr/lib/systemd/systemd ]] || fail 'prepared userspace requires systemd at /usr/lib/systemd/systemd'
install -m 0755 -- "$agent" "$tree/usr/local/libexec/epoch-agent"
install -m 0755 -- "$workload" "$tree/usr/local/libexec/$workload_id"
install -m 0644 -- "$manifest_source" "$tree/etc/epoch/workload.json"
install -m 0644 -- "$recipe_dir/epoch.target" "$tree/etc/systemd/system/epoch.target"
install -m 0644 -- "$recipe_dir/epoch-agent.service" "$tree/etc/systemd/system/epoch-agent.service"
# A minimal explicit target avoids starting inherited application/time services.
[[ ! -d $tree/etc/systemd/system/default.target || -L $tree/etc/systemd/system/default.target ]] || fail 'unexpected default.target directory'
ln -sfnT epoch.target "$tree/etc/systemd/system/default.target"
printf 'root:x:0:0:root:/root:/sbin/nologin\nepoch-workload:x:10001:10001:Epoch fixture:/var/lib/epoch-workload:/sbin/nologin\n' > "$tree/etc/passwd"
printf 'root:x:0:\nepoch-workload:x:10001:\n' > "$tree/etc/group"
printf 'root:!:1:0:99999:7:::\nepoch-workload:!:1:0:99999:7:::\n' > "$tree/etc/shadow"
printf 'root:!::\nepoch-workload:!::\n' > "$tree/etc/gshadow"
printf '# Epoch root is supplied by the kernel; no external mounts.\n' > "$tree/etc/fstab"
chmod 0644 -- "$tree/etc/passwd" "$tree/etc/group" "$tree/etc/fstab"
chmod 0600 -- "$tree/etc/shadow" "$tree/etc/gshadow"
chown 10001:10001 -- "$tree/var/lib/epoch-workload"
chmod 0700 -- "$tree/var/lib/epoch-workload"
for unit in chronyd.service chrony.service ntpd.service ntp.service systemd-timesyncd.service systemd-time-wait-sync.service crond.service cron.service atd.service sshd.service ssh.service timers.target network.target network-online.target; do
  path=$tree/etc/systemd/system/$unit
  # Replacing a unit link is an offline edit inside the verified staging tree.
  if [[ -d $path && ! -L $path ]]; then fail "unexpected unit directory: $unit"; fi
  ln -sfnT /dev/null "$path"
done

image=$stage/rootfs.ext4
(set -o noclobber; : > "$image")
[[ -f $image && ! -L $image && $(realpath -e -- "$image") == "$stage/rootfs.ext4" ]] || fail 'new image-file identity check failed'
truncate -s "$((size_mib * 1024 * 1024))" -- "$image"
mke2fs -q -t ext4 -F -L epoch-root -d "$tree" -- "$image"
e2fsck -fn -- "$image"
install -m 0600 -- "$kernel" "$output/kernel.elf"
install -m 0600 -- "$manifest_source" "$output/workload.json"
mv -T -- "$image" "$output/rootfs.ext4"
kernel_hash=$(sha256sum -- "$output/kernel.elf"); kernel_hash=${kernel_hash%% *}
root_hash=$(sha256sum -- "$output/rootfs.ext4"); root_hash=${root_hash%% *}
agent_hash=$(sha256sum -- "$tree/usr/local/libexec/epoch-agent"); agent_hash=${agent_hash%% *}
installed_workload_hash=$(sha256sum -- "$tree/usr/local/libexec/$workload_id"); installed_workload_hash=${installed_workload_hash%% *}
workload_hash=$(sha256sum -- "$output/workload.json"); workload_hash=${workload_hash%% *}
embedded_workload_hash=$(sha256sum -- "$tree/etc/epoch/workload.json"); embedded_workload_hash=${embedded_workload_hash%% *}
[[ $kernel_hash == "$kernel_input_hash" && $agent_hash == "$agent_input_hash" && $installed_workload_hash == "$workload_input_hash" ]] || fail 'copied kernel or binaries differ from their recorded input hashes'
[[ $workload_hash == "$embedded_workload_hash" ]] || fail 'external workload manifest differs from the installed guest manifest'
if [[ $userspace_kind == squashfs ]]; then
  observed=$(sha256sum -- "$userspace"); observed=${observed%% *}
  [[ $observed == "$userspace_hash" ]] || fail 'squashfs input changed during preparation'
  userspace_transformation="Expanded local squashfs SHA-256 $userspace_hash into new staging with no xattrs"
else
  userspace_transformation='Copied marked operator-prepared directory into retained staging; no whole-directory input digest claimed'
fi
preparation_hash=$(sha256sum -- "$output/preparation-inputs.json"); preparation_hash=${preparation_hash%% *}
jq --arg source "$source_identity" \
  --arg image_id "$image_id" --arg workload_id "$workload_id" --arg workload_version "$workload_version" \
  --arg kh "$kernel_hash" --arg rh "$root_hash" --arg ah "$agent_hash" \
  --arg wh "$workload_hash" --arg prep "$preparation_hash" \
  --arg transformation "$userspace_transformation" \
  --argjson ks "$(stat -c %s -- "$output/kernel.elf")" \
  --argjson rs "$(stat -c %s -- "$output/rootfs.ext4")" \
  -n -f "$recipe_dir/image-manifest.jq" \
  > "$output/$image_id.json"
chmod 0600 -- "$output/rootfs.ext4" "$output/$image_id.json" "$output/preparation-inputs.json"
chown "$owner:$group" -- "$output" "$output/kernel.elf" "$output/rootfs.ext4" "$output/workload.json" "$output/$image_id.json" "$output/preparation-inputs.json"
trap - ERR
printf 'Prepared files at %s; guest boot/vsock/clock compatibility is NOT yet verified.\n' "$output"
