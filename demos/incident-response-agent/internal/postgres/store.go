package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/api"
	"github.com/shanurwan/epoch/demos/incident-response-agent/internal/tools"
)

type Store struct {
	Pool *pgxpool.Pool
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL configuration: %w", err)
	}
	config.MaxConns = 4
	config.MinConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping PostgreSQL: %w", err)
	}
	return &Store{Pool: pool}, nil
}

func (s *Store) Close() {
	if s != nil && s.Pool != nil {
		s.Pool.Close()
	}
}

func (s *Store) ListIncidents(ctx context.Context, status string) ([]api.Incident, error) {
	query := `SELECT id, service, severity, summary, status, created_at
		FROM incidents`
	args := []any{}
	if status != "" {
		query += ` WHERE status = $1`
		args = append(args, status)
	}
	query += ` ORDER BY id`
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()
	incidents := make([]api.Incident, 0)
	for rows.Next() {
		var incident api.Incident
		if err := rows.Scan(&incident.ID, &incident.Service, &incident.Severity, &incident.Summary, &incident.Status, &incident.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		incidents = append(incidents, incident)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate incidents: %w", err)
	}
	return incidents, nil
}

func (s *Store) InspectIncident(ctx context.Context, id string) (api.Incident, []api.Worker, error) {
	var incident api.Incident
	err := s.Pool.QueryRow(ctx, `SELECT id, service, severity, summary, status, created_at
		FROM incidents WHERE id = $1`, id).Scan(
		&incident.ID, &incident.Service, &incident.Severity, &incident.Summary, &incident.Status, &incident.CreatedAt,
	)
	if err != nil {
		return api.Incident{}, nil, fmt.Errorf("inspect incident %q: %w", id, err)
	}
	rows, err := s.Pool.Query(ctx, `SELECT id, service, health, restart_count, updated_at
		FROM workers WHERE service = $1 ORDER BY id`, incident.Service)
	if err != nil {
		return api.Incident{}, nil, fmt.Errorf("list incident workers: %w", err)
	}
	defer rows.Close()
	workers := make([]api.Worker, 0)
	for rows.Next() {
		var worker api.Worker
		if err := rows.Scan(&worker.ID, &worker.Service, &worker.Health, &worker.RestartCount, &worker.UpdatedAt); err != nil {
			return api.Incident{}, nil, fmt.Errorf("scan incident worker: %w", err)
		}
		workers = append(workers, worker)
	}
	if err := rows.Err(); err != nil {
		return api.Incident{}, nil, fmt.Errorf("iterate incident workers: %w", err)
	}
	return incident, workers, nil
}

func (s *Store) Worker(ctx context.Context, id string) (api.Worker, error) {
	var worker api.Worker
	err := s.Pool.QueryRow(ctx, `SELECT id, service, health, restart_count, updated_at
		FROM workers WHERE id = $1`, id).Scan(
		&worker.ID, &worker.Service, &worker.Health, &worker.RestartCount, &worker.UpdatedAt,
	)
	if err != nil {
		return api.Worker{}, fmt.Errorf("read worker %q: %w", id, err)
	}
	return worker, nil
}

func (s *Store) RecordAudit(ctx context.Context, agentID, tool, resource string, decision api.Decision, sideEffect bool) (api.AuditRecord, error) {
	return insertAudit(ctx, s.Pool, agentID, tool, resource, decision, sideEffect)
}

func (s *Store) RestartWorker(ctx context.Context, agentID, workerID, authorityID string) (tools.RestartResult, error) {
	if authorityID == "" {
		return tools.RestartResult{}, errors.New("delegated authority identifier is required")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return tools.RestartResult{}, fmt.Errorf("begin restart transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	before, err := readWorker(ctx, tx, workerID, true)
	if err != nil {
		return tools.RestartResult{}, err
	}
	var insertedID string
	err = tx.QueryRow(ctx, `INSERT INTO authority_uses (jti) VALUES ($1)
		ON CONFLICT DO NOTHING RETURNING jti`, authorityID).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		audit, auditErr := insertAudit(ctx, tx, agentID, tools.WorkerRestart, workerID, api.DenyReplayedAuthority, false)
		if auditErr != nil {
			return tools.RestartResult{}, auditErr
		}
		if err := tx.Commit(ctx); err != nil {
			return tools.RestartResult{}, fmt.Errorf("commit replay audit: %w", err)
		}
		return tools.RestartResult{Before: before, After: before, Audit: audit, Decision: api.DenyReplayedAuthority}, nil
	}
	if err != nil {
		return tools.RestartResult{}, fmt.Errorf("consume delegated authority: %w", err)
	}

	var after api.Worker
	err = tx.QueryRow(ctx, `UPDATE workers
		SET restart_count = restart_count + 1, health = 'healthy', updated_at = clock_timestamp()
		WHERE id = $1
		RETURNING id, service, health, restart_count, updated_at`, workerID).Scan(
		&after.ID, &after.Service, &after.Health, &after.RestartCount, &after.UpdatedAt,
	)
	if err != nil {
		return tools.RestartResult{}, fmt.Errorf("apply simulated worker restart: %w", err)
	}
	audit, err := insertAudit(ctx, tx, agentID, tools.WorkerRestart, workerID, api.Authorized, true)
	if err != nil {
		return tools.RestartResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return tools.RestartResult{}, fmt.Errorf("commit restart transaction: %w", err)
	}
	return tools.RestartResult{
		Before: before, After: after, Audit: audit, Decision: api.Authorized, Applied: true,
	}, nil
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readWorker(ctx context.Context, query queryer, id string, lock bool) (api.Worker, error) {
	statement := `SELECT id, service, health, restart_count, updated_at FROM workers WHERE id = $1`
	if lock {
		statement += ` FOR UPDATE`
	}
	var worker api.Worker
	err := query.QueryRow(ctx, statement, id).Scan(
		&worker.ID, &worker.Service, &worker.Health, &worker.RestartCount, &worker.UpdatedAt,
	)
	if err != nil {
		return api.Worker{}, fmt.Errorf("lock worker %q: %w", id, err)
	}
	return worker, nil
}

func insertAudit(ctx context.Context, query queryer, agentID, tool, resource string, decision api.Decision, sideEffect bool) (api.AuditRecord, error) {
	var audit api.AuditRecord
	err := query.QueryRow(ctx, `INSERT INTO tool_audit
		(agent_id, tool, resource, decision, side_effect_performed)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, agent_id, tool, resource, decision, side_effect_performed, occurred_at`,
		agentID, tool, resource, decision, sideEffect,
	).Scan(&audit.ID, &audit.AgentID, &audit.Tool, &audit.Resource, &audit.Decision, &audit.SideEffectPerformed, &audit.OccurredAt)
	if err != nil {
		return api.AuditRecord{}, fmt.Errorf("record tool audit: %w", err)
	}
	return audit, nil
}
