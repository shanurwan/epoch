package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shanurwan/epoch/demos/incident-response-agent/migrations"
	"github.com/shanurwan/epoch/demos/incident-response-agent/seed"
)

const (
	DatabaseName = "incident_ops"
	DatabaseUser = "incident_app"
)

func InitCluster(ctx context.Context, initDBPath, dataDir string) error {
	if !filepath.IsAbs(initDBPath) || !filepath.IsAbs(dataDir) {
		return errors.New("initdb and data directory paths must be absolute")
	}
	versionPath := filepath.Join(dataDir, "PG_VERSION")
	markerPath := filepath.Join(dataDir, ".epoch-incident-cluster")
	_, versionErr := os.Stat(versionPath)
	_, markerErr := os.Stat(markerPath)
	if versionErr == nil && markerErr == nil {
		return nil
	}
	if (versionErr == nil) != (markerErr == nil) {
		return errors.New("PostgreSQL data directory contains an incomplete prior initialization")
	}
	if !errors.Is(versionErr, os.ErrNotExist) {
		return fmt.Errorf("inspect PostgreSQL version marker: %w", versionErr)
	}
	if !errors.Is(markerErr, os.ErrNotExist) {
		return fmt.Errorf("inspect Epoch cluster marker: %w", markerErr)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("create PostgreSQL data directory: %w", err)
	}
	command := exec.CommandContext(ctx, initDBPath,
		"--pgdata="+dataDir,
		"--encoding=UTF8",
		"--locale=C.UTF-8",
		"--auth-local=reject",
		"--auth-host=reject",
		"--no-instructions",
	)
	command.Stdout = os.Stderr
	command.Stderr = os.Stderr
	command.Env = []string{"LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	if err := command.Run(); err != nil {
		return fmt.Errorf("initialize PostgreSQL cluster: %w", err)
	}
	configuration := `
# Epoch incident-response reference application.
listen_addresses = ''
unix_socket_directories = '/run/postgresql'
unix_socket_group = 'epoch-workload'
unix_socket_permissions = 0770
timezone = 'UTC'
log_timezone = 'UTC'
logging_collector = off
`
	if err := appendFile(filepath.Join(dataDir, "postgresql.conf"), configuration, 0600); err != nil {
		return err
	}
	hostAuthentication := `# Local Unix-socket access only; TCP is disabled.
local all postgres peer
local incident_ops incident_app peer map=incident_map
local all all reject
`
	if err := os.WriteFile(filepath.Join(dataDir, "pg_hba.conf"), []byte(hostAuthentication), 0600); err != nil {
		return fmt.Errorf("write pg_hba.conf: %w", err)
	}
	identityMap := `# Map the constrained workload user to the least-privileged database role.
incident_map epoch-workload incident_app
`
	if err := os.WriteFile(filepath.Join(dataDir, "pg_ident.conf"), []byte(identityMap), 0600); err != nil {
		return fmt.Errorf("write pg_ident.conf: %w", err)
	}
	if err := os.WriteFile(markerPath, []byte("epoch-incident-postgresql/v1\n"), 0600); err != nil {
		return fmt.Errorf("write Epoch cluster marker: %w", err)
	}
	return nil
}

func PrepareDatabase(ctx context.Context, adminDSN string) error {
	admin, err := connectWithRetry(ctx, adminDSN, 30*time.Second)
	if err != nil {
		return err
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `DO $$ BEGIN
		IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'incident_app') THEN
			CREATE ROLE incident_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;
		END IF;
	END $$`); err != nil {
		return fmt.Errorf("create application role: %w", err)
	}
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)`, DatabaseName).Scan(&exists); err != nil {
		return fmt.Errorf("inspect application database: %w", err)
	}
	if !exists {
		if _, err := admin.Exec(ctx, `CREATE DATABASE incident_ops OWNER incident_app TEMPLATE template0 ENCODING 'UTF8'`); err != nil {
			return fmt.Errorf("create application database: %w", err)
		}
	}

	applicationConfig, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return fmt.Errorf("parse application database configuration: %w", err)
	}
	applicationConfig.Database = DatabaseName
	application, err := pgx.ConnectConfig(ctx, applicationConfig)
	if err != nil {
		return fmt.Errorf("connect to application database: %w", err)
	}
	defer application.Close(ctx)
	tx, err := application.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin database preparation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE incident_app`); err != nil {
		return fmt.Errorf("assume application role: %w", err)
	}
	if _, err := tx.Exec(ctx, migrations.Schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if _, err := tx.Exec(ctx, seed.Synthetic); err != nil {
		return fmt.Errorf("reset synthetic data: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit database preparation: %w", err)
	}
	return nil
}

func connectWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*pgx.Conn, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		connection, err := pgx.Connect(ctx, dsn)
		if err == nil {
			return connection, nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("wait for PostgreSQL readiness: %w", lastErr)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func appendFile(path, content string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer file.Close()
	if _, err := file.WriteString(content); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	return nil
}
