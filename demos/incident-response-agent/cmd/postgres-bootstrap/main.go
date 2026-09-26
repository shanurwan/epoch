package main

import (
	"context"
	"fmt"
	"os"

	demopostgres "github.com/shanurwan/epoch/demos/incident-response-agent/internal/postgres"
)

const (
	defaultInitDB = "/usr/lib/postgresql/16/bin/initdb"
	defaultData   = "/var/lib/postgresql/epoch"
	defaultAdmin  = "host=/run/postgresql dbname=postgres user=postgres sslmode=disable"
)

func main() {
	if len(os.Args) != 2 {
		fatal("usage: incident-postgres-bootstrap init|prepare")
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "init":
		err = demopostgres.InitCluster(ctx, envOrDefault("INCIDENT_INITDB", defaultInitDB), envOrDefault("INCIDENT_PGDATA", defaultData))
	case "prepare":
		err = demopostgres.PrepareDatabase(ctx, envOrDefault("INCIDENT_ADMIN_DSN", defaultAdmin))
	default:
		fatal("usage: incident-postgres-bootstrap init|prepare")
	}
	if err != nil {
		fatal(err.Error())
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "incident-postgres-bootstrap:", message)
	os.Exit(1)
}
