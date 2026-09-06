# Only the listed synthetic hardware experiments are accepted by this predicate.
# $expected contains the exact portable scenario used for the run.
def seconds:
  capture("^(?<date>[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2})(?<fraction>\\.[0-9]{1,9})?Z$") as $t
  | (($t.date + "Z" | fromdateiso8601) +
     (if $t.fraction == null or $t.fraction == "" then 0 else ("0" + $t.fraction | tonumber) end));
def sample:
  try (type == "object" and
       (.realtime | seconds | type == "number") and
       (.monotonic_ns | type == "number") and
       .monotonic_ns >= 0 and (.monotonic_ns | floor) == .monotonic_ns) catch false;
def interval:
  (.before | sample) and (.after | sample) and
  .after.monotonic_ns >= .before.monotonic_ns and
  (.after.realtime | seconds) >= (.before.realtime | seconds);
def valid_set($declared):
  (.before | sample) and (.after | sample) and
  .target == $declared.at and .readback_valid == true and
  .tolerance_ms == ($declared.tolerance_ms // 1000) and
  .elapsed_ns >= 0 and .elapsed_ns == (.after.monotonic_ns - .before.monotonic_ns) and
  (.after.realtime | seconds) >= ((.target | seconds) - .tolerance_ms / 1000) and
  (.after.realtime | seconds) <= ((.target | seconds) + .elapsed_ns / 1000000000 + .tolerance_ms / 1000);
def normal_since($anchor; $tolerance):
  all((.before, .after);
    .monotonic_ns >= $anchor.monotonic_ns and
    ((.realtime | seconds) - ($anchor.realtime | seconds) -
      (.monotonic_ns - $anchor.monotonic_ns) / 1000000000 | fabs) <= $tolerance);

. as $report | $expected[0] as $source |
try (
  .api_version == "epoch-result/v1" and
  (.run_id | test("^[0-9a-f]{32}$")) and
  .scenario_name == $source.name and .temporal_mode == "guest-wall-clock-step/v1" and
  .stage == "FINISHED" and .cleanup_status == "COMPLETE" and
  (.started_at | seconds | type == "number") and
  (.finished_at | seconds | type == "number") and .elapsed_ns >= 0 and
  (.steps | map({id, op})) == ($source.steps | map({id, op})) and
  if $scenario | startswith("fault-") then
    .execution_status == "ERROR" and .assertion_status == "NOT_EVALUATED" and
    .assertions == [] and (.steps | length) == 2 and
    .steps[0].id == "initial" and .steps[0].op == "clock.set" and .steps[0].error == null and
    (.steps[0].result | valid_set($source.steps[0])) and
    .steps[1].id == "probe" and .steps[1].op == "action.exec" and .steps[1].result == null and
    (.steps[1].error as $failure |
      ($failure | type) == "object" and
      .causes == [($failure.code + ": " + $failure.message)] and
      ($failure.details | interval) and
      ($failure.details | normal_since($report.steps[0].result.after; $report.steps[0].result.tolerance_ms / 1000)) and
      $failure.details.stdout_json == null and
      if $scenario == "fault-timeout" then
        $source.steps[1].action == "hang" and
        $failure.code == "execution_error" and
        $failure.message == "action deadline/cancellation: context deadline exceeded" and
        $failure.details.stdout_truncated == false and $failure.details.stderr_truncated == false and
        $failure.details.stderr == "" and
        ($failure.details.after.monotonic_ns - $failure.details.before.monotonic_ns) >= (($source.steps[1].timeout_ms - 50) * 1000000) and
        ($failure.details.after.monotonic_ns - $failure.details.before.monotonic_ns) <= (($source.steps[1].timeout_ms + 4000) * 1000000)
      elif $scenario == "fault-large-output" then
        $source.steps[1].action == "large-output" and $failure.code == "output_quota" and
        ($failure.message == "action stdout exceeded 64 KiB" or $failure.message == "action output exceeded quota") and
        $failure.details.stdout_truncated == true and $failure.details.stderr_truncated == false and
        $failure.details.stderr == ""
      elif $scenario == "fault-large-stderr" then
        $source.steps[1].action == "large-stderr" and $failure.code == "output_quota" and
        ($failure.message == "action stderr exceeded 64 KiB" or $failure.message == "action output exceeded quota") and
        $failure.details.stdout_truncated == false and $failure.details.stderr_truncated == true and
        ($failure.details.stderr | utf8bytelength) == 65536
      elif $scenario == "fault-malformed-json" then
        $source.steps[1].action == "malformed-json" and $failure.code == "execution_error" and
        ($failure.message | startswith("action required JSON output: ")) and
        $failure.details.exit_code == 0 and $failure.details.stdout_truncated == false and
        $failure.details.stderr_truncated == false and $failure.details.stderr == ""
      else false end)
  else
    .execution_status == "COMPLETED" and .assertion_status == "PASS" and .causes == [] and
    (.steps | all(.error == null and .result != null)) and
    (.assertions | all(.status == "PASS")) and
    (.assertions | length) == (($source.assertions | length) + ([$source.steps[] | select(.op == "action.exec")] | length)) and
    all($source.assertions[]; . as $a | [$report.assertions[] | select(.id == $a.id and .step == $a.step)] | length == 1) and
    all(range(0; $source.steps | length); . as $i |
      if $source.steps[$i].op == "clock.set" then $report.steps[$i].result | valid_set($source.steps[$i])
      elif $source.steps[$i].op == "clock.read" then $report.steps[$i].result | sample
      else $report.steps[$i].result | interval end)
  end
) catch false
