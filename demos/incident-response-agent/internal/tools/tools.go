package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/authority"
)

const (
	IncidentList    = "incident_list"
	IncidentInspect = "incident_inspect"
	WorkerStatus    = "worker_status"
	WorkerRestart   = "worker_restart"
)

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type RestartResult struct {
	Before   api.Worker
	After    api.Worker
	Audit    api.AuditRecord
	Decision api.Decision
	Applied  bool
}

type Store interface {
	ListIncidents(context.Context, string) ([]api.Incident, error)
	InspectIncident(context.Context, string) (api.Incident, []api.Worker, error)
	Worker(context.Context, string) (api.Worker, error)
	RecordAudit(context.Context, string, string, string, api.Decision, bool) (api.AuditRecord, error)
	RestartWorker(context.Context, string, string, string) (RestartResult, error)
}

type Service struct {
	Store     Store
	Validator authority.Validator
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
}

func NewMCPServer(service *Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "incident-ops-mcp", Version: "v1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: IncidentList, Description: "List synthetic operational incidents."}, service.incidentList)
	mcp.AddTool(server, &mcp.Tool{Name: IncidentInspect, Description: "Inspect one incident and its associated workers."}, service.incidentInspect)
	mcp.AddTool(server, &mcp.Tool{Name: WorkerStatus, Description: "Read one worker's database-backed status."}, service.workerStatus)
	mcp.AddTool(server, &mcp.Tool{Name: WorkerRestart, Description: "Perform an authorized simulated worker restart transaction."}, service.workerRestart)
	return server
}

func (s *Service) now() time.Time {
	if s.Now == nil {
		return time.Now().UTC()
	}
	return s.Now().UTC()
}

func (s *Service) sleep(ctx context.Context, duration time.Duration) error {
	if s.Sleep != nil {
		return s.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Service) incidentList(ctx context.Context, _ *mcp.CallToolRequest, input api.IncidentListInput) (*mcp.CallToolResult, api.IncidentListOutput, error) {
	if input.Status != "" && input.Status != "active" && input.Status != "resolved" {
		return nil, api.IncidentListOutput{}, errors.New("unsupported incident status filter")
	}
	incidents, err := s.Store.ListIncidents(ctx, input.Status)
	return nil, api.IncidentListOutput{Incidents: incidents}, err
}

func (s *Service) incidentInspect(ctx context.Context, _ *mcp.CallToolRequest, input api.IncidentInspectInput) (*mcp.CallToolResult, api.IncidentInspectOutput, error) {
	if !safeID.MatchString(input.IncidentID) {
		return nil, api.IncidentInspectOutput{}, errors.New("invalid incident identifier")
	}
	incident, workers, err := s.Store.InspectIncident(ctx, input.IncidentID)
	return nil, api.IncidentInspectOutput{Incident: incident, Workers: workers}, err
}

func (s *Service) workerStatus(ctx context.Context, _ *mcp.CallToolRequest, input api.WorkerStatusInput) (*mcp.CallToolResult, api.WorkerStatusOutput, error) {
	if !safeID.MatchString(input.WorkerID) {
		return nil, api.WorkerStatusOutput{}, errors.New("invalid worker identifier")
	}
	worker, err := s.Store.Worker(ctx, input.WorkerID)
	return nil, api.WorkerStatusOutput{Worker: worker}, err
}

func (s *Service) workerRestart(ctx context.Context, _ *mcp.CallToolRequest, input api.WorkerRestartInput) (*mcp.CallToolResult, api.WorkerRestartOutput, error) {
	if !safeID.MatchString(input.WorkerID) || input.AuthorityToken == "" || input.ExecutionDelayMS < 0 || input.ExecutionDelayMS > 30000 {
		return nil, api.WorkerRestartOutput{}, errors.New("invalid worker_restart input")
	}
	initialTime := s.now()
	initial := s.Validator.Evaluate(input.AuthorityToken, initialTime, "worker.restart", input.WorkerID)
	before, err := s.Store.Worker(ctx, input.WorkerID)
	if err != nil {
		return nil, api.WorkerRestartOutput{}, err
	}
	result := api.WorkerRestartOutput{
		AgentID:          s.Validator.ExpectedSubject,
		Tool:             WorkerRestart,
		Resource:         input.WorkerID,
		InitialCheck:     api.AuthorizationCheck{Time: initialTime, Decision: initial.Decision},
		ExecutionDelayMS: input.ExecutionDelayMS,
		Worker: api.WorkerTransition{
			RestartCountBefore: before.RestartCount,
			RestartCountAfter:  before.RestartCount,
			HealthBefore:       before.Health,
			HealthAfter:        before.Health,
		},
	}
	if initial.Decision != api.Authorized {
		result.Audit, err = s.Store.RecordAudit(ctx, result.AgentID, WorkerRestart, input.WorkerID, initial.Decision, false)
		return nil, result, err
	}
	if err = s.sleep(ctx, time.Duration(input.ExecutionDelayMS)*time.Millisecond); err != nil {
		return nil, api.WorkerRestartOutput{}, err
	}
	executionTime := s.now()
	if executionTime.Before(initialTime) {
		return nil, api.WorkerRestartOutput{}, fmt.Errorf("guest wall clock regressed during worker_restart")
	}
	result.WallClockElapsedMS = executionTime.Sub(initialTime).Milliseconds()
	execution := s.Validator.Evaluate(input.AuthorityToken, executionTime, "worker.restart", input.WorkerID)
	result.ExecutionCheck = &api.AuthorizationCheck{Time: executionTime, Decision: execution.Decision}
	if execution.Decision != api.Authorized {
		after, workerErr := s.Store.Worker(ctx, input.WorkerID)
		if workerErr != nil {
			return nil, api.WorkerRestartOutput{}, workerErr
		}
		result.Worker.RestartCountAfter = after.RestartCount
		result.Worker.HealthAfter = after.Health
		result.Audit, err = s.Store.RecordAudit(ctx, result.AgentID, WorkerRestart, input.WorkerID, execution.Decision, false)
		return nil, result, err
	}
	transaction, err := s.Store.RestartWorker(ctx, result.AgentID, input.WorkerID, execution.Claims.ID)
	if err != nil {
		return nil, api.WorkerRestartOutput{}, err
	}
	result.ExecutionCheck.Decision = transaction.Decision
	result.SideEffectPerformed = transaction.Applied
	result.Worker = api.WorkerTransition{
		RestartCountBefore: transaction.Before.RestartCount,
		RestartCountAfter:  transaction.After.RestartCount,
		HealthBefore:       transaction.Before.Health,
		HealthAfter:        transaction.After.Health,
	}
	result.Audit = transaction.Audit
	return nil, result, nil
}
