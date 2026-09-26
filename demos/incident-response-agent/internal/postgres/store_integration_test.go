package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/migrations"
	"github.com/shanurwan/epoch/demos/incident-response-agent/seed"
)

func TestRestartTransactionAgainstPostgreSQL(t *testing.T) {
	dsn := os.Getenv("INCIDENT_TEST_DATABASE_DSN")
	if dsn == "" {
		t.Skip("set INCIDENT_TEST_DATABASE_DSN to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, migrations.Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, seed.Synthetic); err != nil {
		t.Fatal(err)
	}
	store := &Store{Pool: pool}
	first, err := store.RestartWorker(ctx, "incident-agent-01", "payments-worker-17", "integration-jti")
	if err != nil {
		t.Fatal(err)
	}
	if first.Decision != api.Authorized || !first.Applied || first.Before.RestartCount != 0 || first.After.RestartCount != 1 {
		t.Fatalf("unexpected authorized transaction: %+v", first)
	}
	second, err := store.RestartWorker(ctx, "incident-agent-01", "payments-worker-17", "integration-jti")
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision != api.DenyReplayedAuthority || second.Applied || second.After.RestartCount != 1 {
		t.Fatalf("unexpected replay transaction: %+v", second)
	}
	var restartCount int
	var auditCount, performedCount int
	if err := pool.QueryRow(ctx, `SELECT restart_count FROM workers WHERE id = 'payments-worker-17'`).Scan(&restartCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE side_effect_performed) FROM tool_audit`).Scan(&auditCount, &performedCount); err != nil {
		t.Fatal(err)
	}
	if restartCount != 1 || auditCount != 2 || performedCount != 1 {
		t.Fatalf("database proof restart_count=%d audit_count=%d performed_count=%d", restartCount, auditCount, performedCount)
	}
}
