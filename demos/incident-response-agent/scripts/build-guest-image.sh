#!/usr/bin/env bash
# Demo-owned offline extension of Epoch's generic single-workload guest recipe.
set -euo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: build-guest-image.sh --apply PREPARED_POSTGRES_USERSPACE KERNEL EPOCH_AGENT DEMO_BIN_DIR OUTPUT_DIR SOURCE_IDENTITY [SIZE_MIB]

Requires Linux x86_64, explicit administrator execution and the same offline
tools as Epoch's generic build-guest-image.sh. The prepared userspace must
already contain PostgreSQL 16; this script never downloads or installs packages.
OUTPUT_DIR must be new and satisfy the generic Epoch recipe's path rules.
USAGE
}
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ ${1:-} == --apply && $# -ge 7 && $# -le 8 ]] || { usage; exit 2; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Linux x86_64 is required'
[[ $EUID -eq 0 ]] || fail 'explicit administrator execution is required; the script does not invoke sudo'
for command in realpath stat install mkdir truncate mke2fs e2fsck jq sha256sum readelf find grep mv cut cat; do
  command -v "$command" >/dev/null || fail "missing preparation tool: $command"
done

demo_dir=$(realpath -e -- "$(dirname -- "${BASH_SOURCE[0]}")/..")
repo_dir=$(realpath -e -- "$demo_dir/../..")
userspace=$(realpath -e -- "$2")
kernel=$(realpath -e -- "$3")
epoch_agent=$(realpath -e -- "$4")
bin_dir=$(realpath -e -- "$5")
output=$6
source_identity=$7
size_mib=${8:-3072}
manifest=$demo_dir/guest/workload.json
image_id=epoch-incident-response-agent-v1

[[ -d $bin_dir && ! -L $bin_dir ]] || fail 'DEMO_BIN_DIR must be a real directory'
for name in incident-response-agent incident-ops-mcp incident-authority-issuer incident-postgres-bootstrap; do
  file=$bin_dir/$name
  [[ -f $file && ! -L $file ]] || fail "missing demo binary: $name"
  readelf -h "$file" | grep -q 'Advanced Micro Devices X86-64' || fail "demo binary is not x86_64 ELF: $name"
done

bash "$repo_dir/scripts/build-guest-image.sh" --apply \
  "$userspace" "$kernel" "$epoch_agent" \
  "$bin_dir/incident-response-agent" "$manifest" \
  "$image_id" "$output" "$source_identity" "$size_mib"

output=$(realpath -e -- "$output")
mapfile -d '' stages < <(find "$output" -mindepth 1 -maxdepth 1 -type d -name '.staging.*' -print0)
[[ ${#stages[@]} -eq 1 ]] || fail 'generic image staging directory is unavailable or ambiguous'
stage=$(realpath -e -- "${stages[0]}")
tree=$(realpath -e -- "$stage/root")
[[ $tree == "$stage/root" && ! -L $tree ]] || fail 'staged root identity check failed'

safe_target() {
  local relative=$1 part current=$tree
  [[ $relative != /* && $relative != *..* ]] || fail 'unsafe staged relative path'
  IFS=/ read -ra parts <<< "$relative"
  for part in "${parts[@]}"; do
    current=$current/$part
    [[ ! -L $current ]] || fail "image-provided symlink blocks demo staging edit: $relative"
  done
}

for path in \
  usr/lib/postgresql/16/bin/postgres \
  usr/lib/postgresql/16/bin/initdb; do
  [[ -x $tree/$path && ! -L $tree/$path ]] || fail "prepared userspace lacks PostgreSQL 16 component: $path"
done
for directory in opt opt/epoch opt/epoch/bin var/lib/postgresql; do
  safe_target "$directory"
  mkdir -p -- "$tree/$directory"
done
chown 0:0 -- "$tree/opt" "$tree/opt/epoch" "$tree/opt/epoch/bin"
chmod 0755 -- "$tree/opt" "$tree/opt/epoch" "$tree/opt/epoch/bin"
chown 10002:10002 -- "$tree/var/lib/postgresql"
chmod 0700 -- "$tree/var/lib/postgresql"

install -m 0755 -- "$bin_dir/incident-ops-mcp" "$tree/opt/epoch/bin/incident-ops-mcp"
install -m 0755 -- "$bin_dir/incident-authority-issuer" "$tree/opt/epoch/bin/incident-authority-issuer"
install -m 0755 -- "$bin_dir/incident-postgres-bootstrap" "$tree/opt/epoch/bin/incident-postgres-bootstrap"
install -m 0644 -- "$demo_dir/guest/epoch.target" "$tree/etc/systemd/system/epoch.target"
install -m 0644 -- "$demo_dir/guest/epoch-agent.service" "$tree/etc/systemd/system/epoch-agent.service"
install -m 0644 -- "$demo_dir/guest/incident-postgres.service" "$tree/etc/systemd/system/incident-postgres.service"
install -m 0644 -- "$demo_dir/guest/incident-postgres-ready.service" "$tree/etc/systemd/system/incident-postgres-ready.service"

cat > "$tree/etc/passwd" <<'EOF'
root:x:0:0:root:/root:/sbin/nologin
epoch-workload:x:10001:10001:Epoch fixture:/var/lib/epoch-workload:/sbin/nologin
postgres:x:10002:10002:PostgreSQL:/var/lib/postgresql:/sbin/nologin
EOF
cat > "$tree/etc/group" <<'EOF'
root:x:0:
epoch-workload:x:10001:postgres
postgres:x:10002:
EOF
cat > "$tree/etc/shadow" <<'EOF'
root:!:1:0:99999:7:::
epoch-workload:!:1:0:99999:7:::
postgres:!:1:0:99999:7:::
EOF
cat > "$tree/etc/gshadow" <<'EOF'
root:!::
epoch-workload:!::postgres
postgres:!::
EOF
chmod 0644 -- "$tree/etc/passwd" "$tree/etc/group"
chmod 0600 -- "$tree/etc/shadow" "$tree/etc/gshadow"

image=$stage/rootfs.ext4
(set -o noclobber; : > "$image")
truncate -s "$((size_mib * 1024 * 1024))" -- "$image"
mke2fs -q -t ext4 -F -L epoch-root -d "$tree" -- "$image"
e2fsck -fn -- "$image"
mv -fT -- "$image" "$output/rootfs.ext4"

root_hash=$(sha256sum -- "$output/rootfs.ext4"); root_hash=${root_hash%% *}
root_size=$(stat -c %s -- "$output/rootfs.ext4")
manifest_path=$output/$image_id.json
manifest_tmp=$output/.$image_id.json.tmp
jq --arg rh "$root_hash" --argjson rs "$root_size" \
  '.rootfs.sha256 = $rh |
   .rootfs.size_bytes = $rs |
   .recipe.revision = "epoch-incident-response-postgres/v1" |
   .recipe.transformations += [
     "Demo extension installed isolated MCP server, authority issuer and PostgreSQL bootstrap binaries",
     "Added dedicated PostgreSQL UID/GID 10002 with Unix-socket-only startup before guest-agent readiness",
     "PostgreSQL schema and synthetic seed reset on every independent guest boot",
     "Rebuilt the private ext4 image offline; guest boot and application behavior remain unverified"
   ]' "$manifest_path" > "$manifest_tmp"
mv -fT -- "$manifest_tmp" "$manifest_path"

demo_provenance=$output/demo-provenance.json
jq -n \
  --arg module 'demos/incident-response-agent' \
  --arg agent "$(sha256sum -- "$tree/usr/local/libexec/incident-response-agent" | cut -d' ' -f1)" \
  --arg server "$(sha256sum -- "$tree/opt/epoch/bin/incident-ops-mcp" | cut -d' ' -f1)" \
  --arg issuer "$(sha256sum -- "$tree/opt/epoch/bin/incident-authority-issuer" | cut -d' ' -f1)" \
  --arg bootstrap "$(sha256sum -- "$tree/opt/epoch/bin/incident-postgres-bootstrap" | cut -d' ' -f1)" \
  --arg postgres "$(sha256sum -- "$tree/usr/lib/postgresql/16/bin/postgres" | cut -d' ' -f1)" \
  --arg initdb "$(sha256sum -- "$tree/usr/lib/postgresql/16/bin/initdb" | cut -d' ' -f1)" \
  '{api_version:"epoch-demo-provenance/v1", module:$module, sha256:{agent:$agent, mcp_server:$server, authority_issuer:$issuer, postgres_bootstrap:$bootstrap, postgres:$postgres, initdb:$initdb}}' \
  > "$demo_provenance"

owner=$(stat -c %u -- "$output")
group=$(stat -c %g -- "$output")
chmod 0600 -- "$output/rootfs.ext4" "$manifest_path" "$demo_provenance"
chown "$owner:$group" -- "$output/rootfs.ext4" "$manifest_path" "$demo_provenance"
printf 'Prepared demo image at %s; guest boot, MCP, PostgreSQL, and clock behavior are NOT yet verified.\n' "$output"
