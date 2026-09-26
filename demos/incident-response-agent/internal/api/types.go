package api

import "time"

type Decision string

const (
	Authorized             Decision = "AUTHORIZED"
	DenyNotYetValid        Decision = "DENY_NOT_YET_VALID"
	DenyExpired            Decision = "DENY_EXPIRED"
	DenyScopeMismatch      Decision = "DENY_SCOPE_MISMATCH"
	DenyResourceMismatch   Decision = "DENY_RESOURCE_MISMATCH"
	DenySubjectMismatch    Decision = "DENY_SUBJECT_MISMATCH"
	DenyInvalidSignature   Decision = "DENY_INVALID_SIGNATURE"
	DenyMalformedAuthority Decision = "DENY_MALFORMED_AUTHORITY"
	DenyReplayedAuthority  Decision = "DENY_REPLAYED_AUTHORITY"
)

type Incident struct {
	ID        string    `json:"id"`
	Service   string    `json:"service"`
	Severity  string    `json:"severity"`
	Summary   string    `json:"summary"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type Worker struct {
	ID           string    `json:"id"`
	Service      string    `json:"service"`
	Health       string    `json:"health"`
	RestartCount int       `json:"restart_count"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AuditRecord struct {
	ID                  int64     `json:"id"`
	AgentID             string    `json:"agent_id"`
	Tool                string    `json:"tool"`
	Resource            string    `json:"resource"`
	Decision            Decision  `json:"decision"`
	SideEffectPerformed bool      `json:"side_effect_performed"`
	OccurredAt          time.Time `json:"occurred_at"`
}

type AuthorizationCheck struct {
	Time     time.Time `json:"time"`
	Decision Decision  `json:"decision"`
}

type IncidentListInput struct {
	Status string `json:"status,omitempty" jsonschema:"optional incident status filter"`
}

type IncidentListOutput struct {
	Incidents []Incident `json:"incidents"`
}

type IncidentInspectInput struct {
	IncidentID string `json:"incident_id" jsonschema:"incident identifier"`
}

type IncidentInspectOutput struct {
	Incident Incident `json:"incident"`
	Workers  []Worker `json:"workers"`
}

type WorkerStatusInput struct {
	WorkerID string `json:"worker_id" jsonschema:"worker identifier"`
}

type WorkerStatusOutput struct {
	Worker Worker `json:"worker"`
}

type WorkerRestartInput struct {
	WorkerID         string `json:"worker_id" jsonschema:"worker identifier"`
	AuthorityToken   string `json:"authority_token" jsonschema:"Ed25519-signed delegated authority JWT"`
	ExecutionDelayMS int64  `json:"execution_delay_ms" jsonschema:"deliberate elapsed-time delay before execution-time revalidation"`
}

type WorkerTransition struct {
	RestartCountBefore int    `json:"restart_count_before"`
	RestartCountAfter  int    `json:"restart_count_after"`
	HealthBefore       string `json:"health_before"`
	HealthAfter        string `json:"health_after"`
}

type WorkerRestartOutput struct {
	AgentID             string              `json:"agent_id"`
	Tool                string              `json:"tool"`
	Resource            string              `json:"resource"`
	InitialCheck        AuthorizationCheck  `json:"initial_check"`
	ExecutionCheck      *AuthorizationCheck `json:"execution_check,omitempty"`
	ExecutionDelayMS    int64               `json:"execution_delay_ms"`
	WallClockElapsedMS  int64               `json:"wall_clock_elapsed_ms"`
	SideEffectPerformed bool                `json:"side_effect_performed"`
	Worker              WorkerTransition    `json:"worker"`
	Audit               AuditRecord         `json:"audit"`
}

type AgentInput struct {
	Scenario          string    `json:"scenario"`
	AgentID           string    `json:"agent_id"`
	AuthorityScope    string    `json:"authority_scope"`
	AuthorityResource string    `json:"authority_resource"`
	NotBefore         time.Time `json:"not_before"`
	ExpiresAt         time.Time `json:"expires_at"`
	ExecutionDelayMS  int64     `json:"execution_delay_ms"`
}

type AgentDecision struct {
	RestartRequested bool   `json:"restart_requested"`
	Reason           string `json:"reason"`
}

type MCPTrace struct {
	Transport       string   `json:"transport"`
	DiscoveredTools []string `json:"discovered_tools"`
	Calls           []string `json:"calls"`
}

type WorkflowOutput struct {
	Scenario     string               `json:"scenario"`
	AgentID      string               `json:"agent_id"`
	MCP          MCPTrace             `json:"mcp"`
	Incident     Incident             `json:"incident"`
	Decision     AgentDecision        `json:"decision"`
	WorkerBefore Worker               `json:"worker_before"`
	Restart      *WorkerRestartOutput `json:"restart,omitempty"`
	WorkerAfter  Worker               `json:"worker_after"`
}
