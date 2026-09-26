package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/agent"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/authority"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/jsonio"
)

const (
	defaultIssuer = "/opt/epoch/bin/incident-authority-issuer"
	defaultServer = "/opt/epoch/bin/incident-ops-mcp"
	defaultDSN    = "host=/run/postgresql dbname=incident_ops user=incident_app sslmode=disable"
)

var (
	safeLabel    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	safeResource = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

func main() {
	ctx := context.Background()
	if err := run(ctx); err != nil {
		fatal(err.Error())
	}
}

func run(ctx context.Context) error {
	var input api.AgentInput
	if err := jsonio.Decode(os.Stdin, &input); err != nil {
		return fmt.Errorf("invalid workload input")
	}
	if err := validateInput(input); err != nil {
		return err
	}
	issued, err := issueAuthority(ctx, envOrDefault("INCIDENT_AUTHORITY_ISSUER", defaultIssuer), authority.IssueRequest{
		Subject: input.AgentID, Scope: input.AuthorityScope, Resource: input.AuthorityResource,
		NotBefore: input.NotBefore.UTC(), ExpiresAt: input.ExpiresAt.UTC(),
	})
	if err != nil {
		return err
	}

	serverCommand := exec.CommandContext(ctx, envOrDefault("INCIDENT_MCP_SERVER", defaultServer))
	serverCommand.Env = []string{
		"INCIDENT_AUTHORITY_PUBLIC_KEY=" + issued.PublicKey,
		"INCIDENT_AGENT_ID=" + input.AgentID,
		"INCIDENT_DATABASE_DSN=" + envOrDefault("INCIDENT_DATABASE_DSN", defaultDSN),
	}
	serverCommand.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "incident-response-agent", Version: "v1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: serverCommand}, nil)
	if err != nil {
		return fmt.Errorf("connect to MCP server: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close()
		}
	}()

	result, err := agent.Run(ctx, agent.SDKClient{Session: session}, input, issued.Token)
	if err != nil {
		return err
	}
	if err := session.Close(); err != nil {
		return fmt.Errorf("close MCP stdio session: %w", err)
	}
	closed = true
	if err := jsonio.Encode(os.Stdout, result); err != nil {
		return fmt.Errorf("encode structured workload output")
	}
	return nil
}

func validateInput(input api.AgentInput) error {
	if !safeLabel.MatchString(input.Scenario) || !safeLabel.MatchString(input.AgentID) ||
		input.AuthorityScope != "worker.restart" || !safeResource.MatchString(input.AuthorityResource) ||
		!input.NotBefore.Before(input.ExpiresAt) || input.ExecutionDelayMS < 0 || input.ExecutionDelayMS > 30000 {
		return fmt.Errorf("invalid workload input")
	}
	return nil
}

func issueAuthority(ctx context.Context, path string, request authority.IssueRequest) (authority.IssueResult, error) {
	var input bytes.Buffer
	if err := jsonio.Encode(&input, request); err != nil {
		return authority.IssueResult{}, fmt.Errorf("encode authority request: %w", err)
	}
	var output bytes.Buffer
	command := exec.CommandContext(ctx, path, "issue")
	command.Stdin = &input
	command.Stdout = &output
	command.Stderr = os.Stderr
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "TZ=UTC"}
	if err := command.Run(); err != nil {
		return authority.IssueResult{}, fmt.Errorf("issue delegated authority: %w", err)
	}
	if output.Len() > authority.MaxTokenBytes*2 {
		return authority.IssueResult{}, fmt.Errorf("authority response exceeds limit")
	}
	var result authority.IssueResult
	if err := jsonio.Decode(&output, &result); err != nil || result.PublicKey == "" || result.Token == "" {
		return authority.IssueResult{}, fmt.Errorf("decode authority response")
	}
	return result, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "incident-response-agent:", message)
	os.Exit(1)
}
