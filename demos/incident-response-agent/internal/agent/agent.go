package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/tools"
)

type Client interface {
	Discover(context.Context) ([]string, error)
	Call(context.Context, string, any, any) error
}

type SDKClient struct {
	Session *mcp.ClientSession
}

func (c SDKClient) Discover(ctx context.Context) ([]string, error) {
	if c.Session == nil {
		return nil, errors.New("MCP session is unavailable")
	}
	names := make([]string, 0)
	cursor := ""
	for {
		result, err := c.Session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("discover MCP tools: %w", err)
		}
		for _, tool := range result.Tools {
			if tool != nil {
				names = append(names, tool.Name)
			}
		}
		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	sort.Strings(names)
	return names, nil
}

func (c SDKClient) Call(ctx context.Context, name string, input, output any) error {
	if c.Session == nil {
		return errors.New("MCP session is unavailable")
	}
	result, err := c.Session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		return fmt.Errorf("call MCP tool %s: %w", name, err)
	}
	if result.IsError {
		return fmt.Errorf("MCP tool %s returned an application error", name)
	}
	if result.StructuredContent == nil {
		return fmt.Errorf("MCP tool %s omitted structured content", name)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return fmt.Errorf("marshal MCP tool %s result: %w", name, err)
	}
	if err := json.Unmarshal(encoded, output); err != nil {
		return fmt.Errorf("decode MCP tool %s result: %w", name, err)
	}
	return nil
}

func Run(ctx context.Context, client Client, input api.AgentInput, authorityToken string) (api.WorkflowOutput, error) {
	output := api.WorkflowOutput{
		Scenario: input.Scenario,
		AgentID:  input.AgentID,
		MCP:      api.MCPTrace{Transport: "stdio"},
	}
	discovered, err := client.Discover(ctx)
	if err != nil {
		return api.WorkflowOutput{}, err
	}
	discovered = append([]string(nil), discovered...)
	sort.Strings(discovered)
	output.MCP.DiscoveredTools = discovered
	for _, required := range []string{tools.IncidentList, tools.IncidentInspect, tools.WorkerStatus, tools.WorkerRestart} {
		if !contains(discovered, required) {
			return api.WorkflowOutput{}, fmt.Errorf("required MCP tool %q was not discovered", required)
		}
	}

	var listed api.IncidentListOutput
	if err := call(ctx, client, &output, tools.IncidentList, api.IncidentListInput{Status: "active"}, &listed); err != nil {
		return api.WorkflowOutput{}, err
	}
	sort.Slice(listed.Incidents, func(i, j int) bool { return listed.Incidents[i].ID < listed.Incidents[j].ID })
	var selected api.Incident
	for _, incident := range listed.Incidents {
		if incident.Status == "active" && incident.Severity == "critical" {
			selected = incident
			break
		}
	}
	if selected.ID == "" {
		output.Decision = api.AgentDecision{Reason: "NO_ACTIVE_CRITICAL_INCIDENT"}
		return output, nil
	}

	var inspected api.IncidentInspectOutput
	if err := call(ctx, client, &output, tools.IncidentInspect, api.IncidentInspectInput{IncidentID: selected.ID}, &inspected); err != nil {
		return api.WorkflowOutput{}, err
	}
	output.Incident = inspected.Incident
	sort.Slice(inspected.Workers, func(i, j int) bool { return inspected.Workers[i].ID < inspected.Workers[j].ID })
	var selectedWorker api.Worker
	for _, worker := range inspected.Workers {
		if worker.Service == inspected.Incident.Service && worker.Health == "unhealthy" {
			selectedWorker = worker
			break
		}
	}
	if selectedWorker.ID == "" {
		output.Decision = api.AgentDecision{Reason: "NO_UNHEALTHY_ASSOCIATED_WORKER"}
		return output, nil
	}

	var status api.WorkerStatusOutput
	if err := call(ctx, client, &output, tools.WorkerStatus, api.WorkerStatusInput{WorkerID: selectedWorker.ID}, &status); err != nil {
		return api.WorkflowOutput{}, err
	}
	output.WorkerBefore = status.Worker
	if status.Worker.Health != "unhealthy" {
		output.Decision = api.AgentDecision{Reason: "WORKER_NO_LONGER_UNHEALTHY"}
		output.WorkerAfter = status.Worker
		return output, nil
	}
	output.Decision = api.AgentDecision{RestartRequested: true, Reason: "ACTIVE_CRITICAL_INCIDENT_WITH_UNHEALTHY_WORKER"}

	var restart api.WorkerRestartOutput
	if err := call(ctx, client, &output, tools.WorkerRestart, api.WorkerRestartInput{
		WorkerID:         status.Worker.ID,
		AuthorityToken:   authorityToken,
		ExecutionDelayMS: input.ExecutionDelayMS,
	}, &restart); err != nil {
		return api.WorkflowOutput{}, err
	}
	output.Restart = &restart
	var after api.WorkerStatusOutput
	if err := call(ctx, client, &output, tools.WorkerStatus, api.WorkerStatusInput{WorkerID: status.Worker.ID}, &after); err != nil {
		return api.WorkflowOutput{}, err
	}
	output.WorkerAfter = after.Worker
	return output, nil
}

func call(ctx context.Context, client Client, output *api.WorkflowOutput, name string, input, result any) error {
	if err := client.Call(ctx, name, input, result); err != nil {
		return err
	}
	output.MCP.Calls = append(output.MCP.Calls, name)
	return nil
}

func contains(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}
