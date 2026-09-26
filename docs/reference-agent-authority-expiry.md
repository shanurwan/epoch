# Reference experiment: expiring autonomous-agent authority

This is one application workload demonstrating Epoch's broader expiry and
execution-boundary testing model. Epoch core remains application-independent and
contains no autonomous-agent, capability, resource, MCP, external authorization,
LLM, or chatbot model.

## Research question

What happens when an autonomous software agent is authorized to perform an
operation, but that authority expires before the operation is actually executed?

The reference domain is a local incident-operations service. Its privileged
operation is a simulated `worker.restart` for a named worker. A successful
simulation updates only `worker-state.json` in the workload's private guest working
directory. It never invokes `systemctl`, restarts a real service, reaches the host,
or uses the network.

The workload accepts an authority object such as:

```json
{
  "principal": "agent://incident-agent-01",
  "capability": "worker.restart",
  "resource": "worker-17",
  "not_before": "2035-01-01T12:00:00Z",
  "expires_at": "2035-01-01T12:00:30Z"
}
```

It captures the guest wall clock once for each authorization decision and applies
the half-open validity interval:

```text
not_before <= now < expires_at
```

Therefore `now == not_before` is authorized and `now == expires_at` is expired.
Timestamps require an explicit RFC 3339 offset and are normalized to UTC. Unit tests
cover immediately before, exactly at, inside, exactly at expiry, and immediately
after the interval.

Decisions are typed rather than represented by an ambiguous boolean:

- `AUTHORIZED`
- `DENY_NOT_YET_VALID`
- `DENY_EXPIRED`
- `DENY_CAPABILITY_MISMATCH`
- `DENY_RESOURCE_MISMATCH`
- `DENY_MALFORMED_AUTHORITY`
- `DENY_PRINCIPAL_MISMATCH`
- `DENY_MALFORMED_REQUEST`
- `DENY_UNSUPPORTED_OPERATION`

Malformed or mismatched input fails closed. The workload currently permits only
the simulated `worker.restart` operation; broader incident read operations are a
possible extension, not an Epoch-core feature.

## Temporal boundary pair

The two basic scenarios declare the same authority, operation, resource, workload,
software, and base image ID. They differ in guest wall-clock target:

| Scenario | Guest time | Decision | Simulated side effect |
| --- | --- | --- | --- |
| `agent-authority-before-expiry.json` | `12:00:29Z` | `AUTHORIZED` | performed |
| `agent-authority-after-expiry.json` | `12:00:31Z` | `DENY_EXPIRED` | not performed |

These are separate cold-boot temporal executions with fresh private disk copies.
They do not claim identical VM memory or snapshot restore. The one-second boundary
case intentionally matches the research example; the scenario uses a 100 ms clock
readback tolerance.

## TOCTOU revalidation

`agent-authority-toctou.json` separates request acceptance from privileged
execution:

```text
12:00:25  request arrives       -> initial_authorization = AUTHORIZED
12:00:30  authority expires
12:00:35  execution boundary    -> execution_authorization = DENY_EXPIRED
                                  side_effect_performed = false
```

The `request` action persists one pending request in workload-local state. The
`execute-pending` action consumes it and evaluates the complete authority again
against the then-current guest clock immediately before the simulated side effect.
It does not treat the earlier authorization as durable permission. The final
`worker-state` observation asserts that the restart count remains zero.

Epoch recovery cannot replay either action. The guest protocol's existing rule for
lost state-changing responses still applies: the outcome is unknown and execution
ends with an error.

## Validation status

The authority evaluator, exact interval boundaries, execution-time revalidation,
and workload-local state behavior are implemented and unit-tested. At revision
`5f0f89f03f0660e511dee13e49a28b105aea7684`, all three portable scenarios also
ran successfully in real Firecracker guests on the recorded bare-metal Rocky
Linux host:

| Scenario | Observed semantic outcome | Final outcomes |
| --- | --- | --- |
| Before expiry | `AUTHORIZED`; side effect performed; restart count 1 | `COMPLETED` / `PASS` / `COMPLETE` |
| After expiry | `DENY_EXPIRED`; no side effect | `COMPLETED` / `PASS` / `COMPLETE` |
| TOCTOU | initial `AUTHORIZED`; execution `DENY_EXPIRED`; no side effect; restart count 0 | `COMPLETED` / `PASS` / `COMPLETE` |

Twenty fresh executions of each scenario produced one normalized semantic-result
hash per scenario with 20/20 execution, assertion, and cleanup passes. The
normalization retains decisions, side effects, workload state, declared temporal
targets, assertion statuses, and outcome dimensions while excluding volatile run
IDs, timestamps, and durations. See the
[hardware evidence](../evidence/rocky-linux-x86_64/2026-09-20/summary.md).

This is a scoped result for these workloads, images, host, and normalized fields.
It is not a reliability percentage, a general deterministic-execution claim, or
evidence of snapshot-backed forks.
