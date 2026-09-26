TRUNCATE TABLE tool_audit, authority_uses, workers, incidents RESTART IDENTITY;

INSERT INTO incidents (id, service, severity, summary, status, created_at)
VALUES (
    'INC-2026-001',
    'payments-api',
    'critical',
    'Elevated payment request failures',
    'active',
    '2035-01-01T11:50:00Z'
);

INSERT INTO workers (id, service, health, restart_count, updated_at)
VALUES (
    'payments-worker-17',
    'payments-api',
    'unhealthy',
    0,
    '2035-01-01T11:50:00Z'
);
