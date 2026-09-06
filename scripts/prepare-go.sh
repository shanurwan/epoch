#!/usr/bin/env bash
# Explicit user-local toolchain preparation. No system install or profile edits.
set -euo pipefail
umask 077
[[ $# == 2 && $1 == --install-dir ]] || { printf 'Usage: bash scripts/prepare-go.sh --install-dir ABSOLUTE_NEW_DIRECTORY\n' >&2; exit 2; }
target=$2
[[ $EUID != 0 && $(uname -sm) == 'Linux x86_64' ]] || { printf 'Requires ordinary-user Linux x86_64\n' >&2; exit 1; }
[[ $target == /* && $target != / && ! -e $target && ! -L $target ]] || { printf 'Install target must be a new absolute directory\n' >&2; exit 1; }
parent=$(dirname -- "$target")
[[ -d $parent && $(realpath -e -- "$parent") == "$parent" ]] || { printf 'Existing canonical parent required\n' >&2; exit 1; }
mkdir -m 0700 -- "$target"
archive=$target/go1.26.8.linux-amd64.tar.gz
curl --fail --location --proto '=https' --tlsv1.2 --output "$archive" 'https://go.dev/dl/go1.26.8.linux-amd64.tar.gz'
printf '%s  %s\n' 'd0f743b33e8d8945e6b1f432edd15785c70507121d6e2a723b21285eddf8b57b' "$archive" | sha256sum --check -
tar -xzf "$archive" -C "$target" --no-same-owner
"$target/go/bin/go" version
printf 'Invoke %s/go/bin/go explicitly; no system files or login profiles changed.\n' "$target"
