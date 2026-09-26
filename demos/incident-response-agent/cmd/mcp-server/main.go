package main

import (
	"context"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/authority"
	demopostgres "github.com/shanurwan/epoch/demos/incident-response-agent/internal/postgres"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/tools"
)

const defaultDSN = "host=/run/postgresql dbname=incident_ops user=incident_app sslmode=disable"

func main() {
	if err := run(context.Background()); err != nil {
		fatal(err.Error())
	}
}

func run(ctx context.Context) error {
	publicKey, err := authority.DecodePublicKey(os.Getenv("INCIDENT_AUTHORITY_PUBLIC_KEY"))
	if err != nil {
		return fmt.Errorf("authority public key is unavailable")
	}
	subject := os.Getenv("INCIDENT_AGENT_ID")
	if subject == "" {
		return fmt.Errorf("expected agent subject is unavailable")
	}
	dsn := os.Getenv("INCIDENT_DATABASE_DSN")
	if dsn == "" {
		dsn = defaultDSN
	}
	store, err := demopostgres.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer store.Close()
	server := tools.NewMCPServer(&tools.Service{
		Store: store,
		Validator: authority.Validator{
			PublicKey:       publicKey,
			ExpectedSubject: subject,
		},
	})
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("MCP stdio server failed: %w", err)
	}
	return nil
}

func fatal(message string) {
	// stdout belongs exclusively to the MCP stdio transport.
	fmt.Fprintln(os.Stderr, "incident-ops-mcp:", message)
	os.Exit(1)
}
