#!/usr/bin/env bash
# Offline only: overlay operator-supplied Debian packages onto a local userspace.
set -euo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: prepare-postgres-userspace.sh --apply BASE_SQUASHFS DEB_DIR OUTPUT_DIR

BASE_SQUASHFS and every PostgreSQL/runtime dependency package must already be
local. No repositories are contacted and no package maintainer scripts run.
OUTPUT_DIR must be new, absolute and beneath a mode-0700 non-root-owned parent.
USAGE
}
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ ${1:-} == --apply && $# -eq 4 ]] || { usage; exit 2; }
[[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'Linux x86_64 is required'
[[ $EUID -eq 0 ]] || fail 'explicit administrator execution is required; the script does not invoke sudo'
for command in realpath stat mkdir unsquashfs dpkg-deb find sort sha256sum jq chown chmod rm; do
  command -v "$command" >/dev/null || fail "missing preparation tool: $command"
done

[[ ! -L $2 && -f $2 ]] || fail 'BASE_SQUASHFS must be a regular non-symlink file'
base=$(realpath -e -- "$2")
[[ ! -L $3 && -d $3 ]] || fail 'DEB_DIR must be a real directory'
deb_dir=$(realpath -e -- "$3")
output=$4
[[ $output == /* && ! -e $output && ! -L $output ]] || fail 'OUTPUT_DIR must be a new absolute path'
parent=$(realpath -e -- "$(dirname -- "$output")")
[[ $output == "$parent/$(basename -- "$output")" ]] || fail 'OUTPUT_DIR must be canonical without traversal'
[[ $(stat -c %u -- "$parent") -ne 0 && $(stat -c %a -- "$parent") == 700 ]] || fail 'OUTPUT_DIR parent must be mode 0700 and owned by the ordinary operator'
owner=$(stat -c %u -- "$parent")
group=$(stat -c %g -- "$parent")

mapfile -d '' packages < <(find "$deb_dir" -mindepth 1 -maxdepth 1 -type f -name '*.deb' -print0 | sort -z)
[[ ${#packages[@]} -gt 0 ]] || fail 'DEB_DIR contains no regular .deb packages'
for package in "${packages[@]}"; do
  [[ ! -L $package && $(basename -- "$package") =~ ^[A-Za-z0-9][A-Za-z0-9.+_:-]*\.deb$ ]] || fail 'unsafe package path or filename'
  architecture=$(dpkg-deb -f "$package" Architecture)
  [[ $architecture == amd64 || $architecture == all ]] || fail "package is not amd64/all: $(basename -- "$package")"
done

unsquashfs -strict-errors -no-progress -processors 1 -data-queue 16 -frag-queue 16 -no-xattrs -dest "$output" "$base"
trap 'printf "Preparation stopped; inspect retained userspace at %s\n" "$output" >&2' ERR
for package in "${packages[@]}"; do
  dpkg-deb --extract "$package" "$output"
done

for pseudo in dev proc sys run; do
  [[ ! -L $output/$pseudo ]] || fail "symlink pseudo-filesystem path: $pseudo"
  [[ ! -d $output/$pseudo || -z $(find "$output/$pseudo" -mindepth 1 -print -quit) ]] || fail "userspace $pseudo must remain empty"
done
[[ -z $(find "$output" -xdev \( -type b -o -type c -o -type s \) -print -quit) ]] || fail 'prepared userspace contains a device or socket'
for path in usr/lib/postgresql/16/bin/postgres usr/lib/postgresql/16/bin/initdb; do
  [[ -x $output/$path && ! -L $output/$path ]] || fail "prepared userspace lacks PostgreSQL 16 component: $path"
done

package_lines=$output/.epoch-postgres-packages.tsv
: > "$package_lines"
for package in "${packages[@]}"; do
  hash=$(sha256sum -- "$package"); hash=${hash%% *}
  printf '%s\t%s\t%s\t%s\n' \
    "$(dpkg-deb -f "$package" Package)" \
    "$(dpkg-deb -f "$package" Version)" \
    "$hash" \
    "$(basename -- "$package")" >> "$package_lines"
done
jq -Rn '[inputs | split("\t") | {package:.[0], version:.[1], sha256:.[2], file:.[3]}]' \
  < "$package_lines" > "$output/.epoch-postgres-packages.json"
rm -f -- "$package_lines"
base_hash=$(sha256sum -- "$base"); base_hash=${base_hash%% *}
package_manifest_hash=$(sha256sum -- "$output/.epoch-postgres-packages.json"); package_manifest_hash=${package_manifest_hash%% *}
jq -n --arg base_sha256 "$base_hash" --arg packages_sha256 "$package_manifest_hash" \
  '{api_version:"epoch-prepared-userspace/v1", base_sha256:$base_sha256, package_manifest_sha256:$packages_sha256, package_installation:"dpkg-deb-extract-only-no-maintainer-scripts"}' \
  > "$output/.epoch-prepared-userspace"
chmod 0600 -- "$output/.epoch-prepared-userspace" "$output/.epoch-postgres-packages.json"
chown "$owner:$group" -- "$output" "$output/.epoch-prepared-userspace" "$output/.epoch-postgres-packages.json"
trap - ERR
printf 'Prepared offline PostgreSQL userspace at %s; guest compatibility is NOT yet verified.\n' "$output"
