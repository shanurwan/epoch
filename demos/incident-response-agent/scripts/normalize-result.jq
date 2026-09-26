([.steps[] | select(.id == "agent-run")][0].result.stdout_json) as $observation |
{
  scenario: $observation.scenario,
  agent_id: $observation.agent_id,
  transport: $observation.mcp.transport,
  discovered_tools: $observation.mcp.discovered_tools,
  calls: $observation.mcp.calls,
  incident: $observation.incident.id,
  worker: $observation.worker_before.id,
  initial_authorization: $observation.restart.initial_check.decision,
  execution_authorization: ($observation.restart.execution_check.decision // "NOT_REACHED"),
  restart_count_before: $observation.worker_before.restart_count,
  restart_count_after: $observation.worker_after.restart_count,
  health_before: $observation.worker_before.health,
  health_after: $observation.worker_after.health,
  side_effect_performed: $observation.restart.side_effect_performed,
  audit_decision: $observation.restart.audit.decision,
  audit_side_effect_performed: $observation.restart.audit.side_effect_performed,
  execution_status: .execution_status,
  assertion_status: .assertion_status,
  cleanup_status: .cleanup_status
}
