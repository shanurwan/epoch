CREATE TABLE IF NOT EXISTS incidents (
    id          TEXT PRIMARY KEY,
    service     TEXT NOT NULL,
    severity    TEXT NOT NULL,
    summary     TEXT NOT NULL,
    status      TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS workers (
    id             TEXT PRIMARY KEY,
    service        TEXT NOT NULL,
    health         TEXT NOT NULL,
    restart_count  INTEGER NOT NULL DEFAULT 0,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE TABLE IF NOT EXISTS tool_audit (
    id                    BIGSERIAL PRIMARY KEY,
    agent_id              TEXT NOT NULL,
    tool                  TEXT NOT NULL,
    resource              TEXT NOT NULL,
    decision              TEXT NOT NULL,
    side_effect_performed BOOLEAN NOT NULL,
    occurred_at           TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

-- A delegated authority is one-use for the privileged restart transaction.
-- The token itself and private signing material are never stored.
CREATE TABLE IF NOT EXISTS authority_uses (
    jti       TEXT PRIMARY KEY,
    used_at   TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
