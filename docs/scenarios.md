# Scenarios and assertions

Epoch is general-purpose temporal systems-testing infrastructure. The portable
JSON scenario selects a locally prepared image and named workload operations.
Machine paths belong in operator configuration. Validation never boots a VM.

Run `epoch validate scenarios/clock-smoke.json --config epoch.local.json` before
an explicitly approved disposable-VM experiment. See
`schemas/scenario.schema.json` for the structural contract. The Go validator also
checks semantic constraints that JSON Schema cannot express: unique identifiers,
available observation references, total wait budget and service chronology.

## Strict input

The complete scenario is at most 1 MiB. Objects reject duplicate keys, unknown
fields and case variants of field names. A single JSON document is required.
Structural fields cannot be null; action input and assertion values may contain
JSON null. Operation-incompatible fields are rejected even when their value is
false or zero. Nested action input is validated as JSON and is at most 64 KiB.

Nesting is limited to 64 levels. Numeric literals are limited to 256 characters
and exponents between -1024 and 1024. These are conservative parser limits, not
measured capacity claims. Numeric fields used as durations/resources/exit codes
must be ordinary JSON integers; exponent and decimal syntax are rejected there.

Identifiers use 1..64 ASCII letters, digits, dots, underscores or hyphens, starting
with a letter or digit. Image, workload, action and service identifiers exclude
dots to match the guest manifest contract. A scenario contains 1..128 steps and 1..128 explicit
assertions. No expression language or shell assertions are supported.

## Defaults and sequence

Omitted resources default to 1 vCPU and 512 MiB; limits are 2 vCPUs and 2048 MiB,
with a minimum of 128 MiB. A supplied resources object must provide both values.
Machine configuration may impose tighter policy. These are guest settings, not
hard host CPU/RSS containment.

The default scenario budget is 120000 ms and boot budget is 30000 ms. The maximum
scenario budget is 600000 ms. A boot/request timeout cannot exceed the scenario
budget; actual request deadlines are also bounded by the remaining run budget.
Action/service timeouts default to 5000 ms, or the scenario budget when smaller.
This is the guest workload budget. The host permits up to 4000 ms more for bounded
guest process termination and delivery of its typed result/error. Both the guest
budget sent on the wire and the complete host request are clipped to the remaining
scenario budget. Parent cancellation takes effect immediately. When the overall
run budget expires first, a typed guest timeout response is not guaranteed; the
action outcome may remain unknown and cleanup still runs with its separate budget.
A `wait` requires positive `duration_ms`; combined waits must leave time in the
scenario budget. Explicit zero and negative durations/timeouts are rejected.

A `clock.set` must precede any action or service start. Targets use RFC3339 with an
explicit offset and at most nine fractional-second digits, normalize to UTC, and
fall in the half-open interval
1990-01-01T00:00:00Z through 2100-01-01T00:00:00Z. Immediate local readback tolerance
defaults to 1000 ms, with an accepted range of 1..10000 ms. The clock continues to
tick. Waits and deadlines measure real elapsed time.

A clock change while any declared service is running requires `allow_live: true`.
Services cannot start twice or stop before starting; at most four can be running.
Stopping a service allows later clock changes without `allow_live`. These checks
do not claim that a running process was quiesced or that missed jobs execute
automatically.

## Observations and assertions

Assertion pointers address the operation result, not the protocol envelope.
Action results provide `stdout_json`, `exit_code`, `stderr`, truncation flags and
`before`/`after` clock observations. Clock reads provide `realtime` and
`monotonic_ns`. Clock changes provide `target`, `before`, `after`, `elapsed_ns`,
`tolerance_ms` and `readback_valid`. Services provide `service`, `running`,
`before` and `after`, with optional `stdout`/`stderr` diagnostics. Wait steps do
not provide guest assertion observations.

The pointer implementation follows the JSON string form of RFC 6901. Empty string
selects the whole observation; `/` selects an empty object key. `~1` decodes to
`/` and `~0` to `~`, in one pass. URI fragments are not accepted. Array indices
are zero-based decimal integers without leading zeroes. `-` does not identify an
existing array value. Object keys that look numeric remain ordinary keys.

- `eq` and `ne` preserve JSON types, recursively compare arrays/objects, and
  compare numbers exactly as rational values. For example, 0.1 equals 1e-1,
  9007199254740993 differs from 9007199254740992, and 1 differs from "1".
- `gt`, `gte`, `lt` and `lte` require numeric values and use exact comparisons.
- `exists` and `absent` take no `value`. A present JSON null exists.
- A missing required pointer for a value comparison, missing step result,
  malformed required JSON or incompatible numeric observation is an execution
  error. A valid comparison that disagrees with the expected value is an
  assertion failure.

Assertions cannot reference nonexistent steps or fields unavailable for that
operation. Application-defined paths below `stdout_json` are resolved at runtime.
Each action's default expected exit is zero; `expected_exit_code` accepts 0..255.
A normal nonzero exit is an observation. Launch failures, timeouts, output quotas
and transport failures remain execution errors.

## Included experiments

`clock-smoke.json` changes guest time across the 1999/2000 boundary and checks a
persistent marker. `clock-backward.json` checks a backward change while preserving
the marker. `live-service.json` explicitly changes time with a service running.
`real-sleep.json` exercises a fixed real-time sleep.
`expected-nonzero.json` expects the fixture's normal exit code 7.

The `agent-authority-before-expiry` and `agent-authority-after-expiry` reference
scenarios apply the same declared incident-agent authority and simulated
`worker.restart` operation at 12:00:29Z and 12:00:31Z. The
`agent-authority-toctou` scenario accepts the request at 12:00:25Z, moves guest
time past the 12:00:30Z expiry, revalidates at 12:00:35Z, and asserts that no
workload-local restart side effect occurred. These scenarios use the separate
`agent-authority-expiry` workload; no domain fields were added to the scenario
schema or engine. See `reference-agent-authority-expiry.md`.

The `incident-response-mcp-before-expiry`, `incident-response-mcp-after-expiry`,
and `incident-response-mcp-toctou` scenarios exercise the isolated
`demos/incident-response-agent` application through the same generic action and
assertion boundary. That application uses real MCP stdio calls and an in-guest
PostgreSQL side effect; none of its domain types or dependencies are part of
Epoch core. These scenarios were hardware-exercised in the recorded 2026-09-26
working-tree evidence: 20/20 runs per scenario completed with one normalized
semantic result per scenario. This is not yet clean-revision evidence and does
not establish general deterministic execution.

The `fault-timeout`, `fault-large-output`, `fault-large-stderr` and
`fault-malformed-json` scenarios are platform negative controls. Their intended
result is execution ERROR, not an application assertion failure or a passing
Epoch run. A separate opted-in hardware harness can verify those outcomes.
Their presence and passing parser/unit tests do not demonstrate successful guest
boot, actual guest clock changes or host clock isolation.
