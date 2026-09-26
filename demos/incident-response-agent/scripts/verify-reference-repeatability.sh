#!/usr/bin/env bash
# Runs normal foreground Epoch commands. It performs no privileged host action.
set -euo pipefail
umask 077

usage() {
  cat <<'USAGE'
Usage: verify-reference-repeatability.sh --apply EPOCH_BIN CONFIG OUTPUT_DIR REPEAT_COUNT

OUTPUT_DIR must be new. Epoch's configured evidence directory remains the source
of raw run evidence; this directory stores captured reports and semantic hashes.
USAGE
}
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

[[ ${1:-} == --apply && $# -eq 5 ]] || { usage; exit 2; }
for command in realpath mkdir jq sha256sum; do
  command -v "$command" >/dev/null || fail "missing tool: $command"
done
[[ -f $2 && -x $2 && ! -L $2 ]] || fail 'EPOCH_BIN must be an executable regular file'
epoch_bin=$(realpath -e -- "$2")
[[ -f $3 && ! -L $3 ]] || fail 'CONFIG must be a regular non-symlink file'
config=$(realpath -e -- "$3")
output=$4
[[ $output == /* && ! -e $output && ! -L $output ]] || fail 'OUTPUT_DIR must be a new absolute path'
output_parent=$(realpath -e -- "$(dirname -- "$output")")
[[ $output == "$output_parent/$(basename -- "$output")" ]] || fail 'OUTPUT_DIR must be canonical without traversal'
output=$output_parent/$(basename -- "$output")
[[ $5 =~ ^[1-9][0-9]?$|^100$ ]] || fail 'REPEAT_COUNT must be 1..100'
repeat_count=$5

demo_dir=$(realpath -e -- "$(dirname -- "${BASH_SOURCE[0]}")/..")
repo_dir=$(realpath -e -- "$demo_dir/../..")
normalizer=$demo_dir/scripts/normalize-result.jq
mkdir -m 0700 -- "$output"
records=$output/runs.ndjson
: > "$records"
failed=0

for case_name in before-expiry after-expiry toctou; do
  scenario=$repo_dir/scenarios/incident-response-mcp-$case_name.json
  case_dir=$output/$case_name
  mkdir -m 0700 -- "$case_dir"
  for ((run_number=1; run_number<=repeat_count; run_number++)); do
    report=$case_dir/run-$(printf '%03d' "$run_number").json
    set +e
    "$epoch_bin" run "$scenario" --config "$config" --json > "$report"
    exit_code=$?
    set -e
    normalized=$case_dir/run-$(printf '%03d' "$run_number").normalized.json
    if jq -S -c -f "$normalizer" "$report" > "$normalized"; then
      semantic_hash=$(sha256sum -- "$normalized"); semantic_hash=${semantic_hash%% *}
      jq -cn \
        --arg case "$case_name" --arg report "$report" --arg normalized "$normalized" \
        --arg hash "$semantic_hash" --argjson run "$run_number" --argjson exit_code "$exit_code" \
        --slurpfile semantic "$normalized" \
        '{case:$case, run:$run, exit_code:$exit_code, report:$report, normalized:$normalized, semantic_sha256:$hash, semantic:$semantic[0]}' \
        >> "$records"
    else
      failed=1
      jq -cn --arg case "$case_name" --arg report "$report" --argjson run "$run_number" --argjson exit_code "$exit_code" \
        '{case:$case, run:$run, exit_code:$exit_code, report:$report, normalization_error:true}' >> "$records"
    fi
    if (( exit_code != 0 )); then failed=1; fi
  done
done

jq -s '
  group_by(.case) |
  map({
    scenario: .[0].case,
    runs_attempted: length,
    runs_completed: ([.[] | select(.semantic.execution_status == "COMPLETED")] | length),
    execution_passes: ([.[] | select(.semantic.execution_status == "COMPLETED")] | length),
    assertion_passes: ([.[] | select(.semantic.assertion_status == "PASS")] | length),
    cleanup_passes: ([.[] | select(.semantic.cleanup_status == "COMPLETE")] | length),
    unique_normalized_result_hashes: ([.[] | .semantic_sha256 // empty] | unique | length),
    normalized_result_hashes: ([.[] | .semantic_sha256 // empty] | unique),
    failed_run_numbers: [.[] | select(.exit_code != 0 or .normalization_error == true) | .run]
  }) |
  {api_version:"epoch-reference-repeatability/v1", scenarios:.}
' "$records" > "$output/summary.json"

printf 'Repeatability summary: %s\n' "$output/summary.json"
if (( failed != 0 )); then
  printf 'One or more runs failed or could not be normalized; inspect runs.ndjson and raw Epoch evidence.\n' >&2
  exit 1
fi
