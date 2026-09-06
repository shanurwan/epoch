#!/usr/bin/env bash
# Explicit system test harness. Normal builds and go test never invoke this file.
set -euo pipefail
umask 077
[[ $# == 3 && $1 == --allow-disposable-guest-clock ]] || {
  printf 'Usage: bash scripts/test-kvm.sh --allow-disposable-guest-clock ABSOLUTE_EPOCH_BINARY ABSOLUTE_CONFIG\n' >&2
  exit 2
}
[[ $EUID != 0 && $(uname -sm) == 'Linux x86_64' ]] || { printf 'Ordinary-user Linux x86_64 required\n' >&2; exit 1; }
binary=$2
config=$3
[[ $binary == /* && -f $binary && ! -L $binary && -x $binary && $config == /* && -f $config && ! -L $config ]] || { printf 'Explicit regular binary/config paths required\n' >&2; exit 2; }
command -v jq >/dev/null || { printf 'jq is required for evidence validation\n' >&2; exit 1; }
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
"$binary" doctor --config "$config" --json
"$binary" validate scenarios/clock-smoke.json --config "$config"
printf 'Opt-in recorded: boot disposable private disk copies and change only the guest clock.\n'
test_one() {
  local scenario=$1 expected=$2 rc=0 result run_id persisted
  result=$("$binary" run "scenarios/$scenario.json" --config "$config" --json) || rc=$?
  [[ $rc == "$expected" ]] || { printf '%s\n' "$result"; printf '%s: expected exit %s, observed %s\n' "$scenario" "$expected" "$rc" >&2; return 1; }
  if ! jq -e --arg scenario "$scenario" --slurpfile expected "scenarios/$scenario.json" -f scripts/check-kvm-report.jq <<< "$result" >/dev/null; then
    printf '%s\n' "$result"
    printf '%s: report does not establish the declared experiment or precise expected fault\n' "$scenario" >&2
    return 1
  fi
  run_id=$(jq -er '.run_id' <<< "$result")
  persisted=$("$binary" report "$run_id" --config "$config" --json) || return 1
  jq -e --argjson observed "$result" '. == $observed' <<< "$persisted" >/dev/null || {
    printf '%s: persisted report differs from command result\n' "$scenario" >&2
    return 1
  }
  jq -r --arg scenario "$scenario" '$scenario + ": " + .run_id + " execution=" + .execution_status + " assertions=" + .assertion_status + " cleanup=" + .cleanup_status' <<< "$result"
}
test_one clock-smoke 0
test_one clock-backward 0
test_one real-sleep 0
test_one expected-nonzero 0
test_one live-service 0
test_one fault-timeout 3
test_one fault-large-output 3
test_one fault-large-stderr 3
test_one fault-malformed-json 3
"$binary" validate scenarios/clock-smoke.json --config "$config"
printf 'This harness passed the listed experiments only; inspect retained per-run evidence.\n'
