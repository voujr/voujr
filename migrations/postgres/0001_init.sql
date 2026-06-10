-- voujr schema (0001) — Postgres dialect. Mirrors the SQLite schema with
-- Postgres types: TIMESTAMPTZ + now(), BIGSERIAL for the audit-log id, BYTEA for
-- embeddings (brute-force cosine in Go, same as SQLite — no pgvector required),
-- DOUBLE PRECISION for money/scores. The migrator splits this file by statement.

CREATE TABLE IF NOT EXISTS users (
    id          TEXT PRIMARY KEY,
    subject     TEXT NOT NULL,
    email       TEXT,
    role        TEXT NOT NULL DEFAULT 'reader',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS clusters (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    context       TEXT,
    provider      TEXT,
    endpoint_hash TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    id            TEXT PRIMARY KEY,
    user_id       TEXT REFERENCES users(id),
    cluster_id    TEXT REFERENCES clusters(id),
    mode          TEXT NOT NULL DEFAULT 'read-only',
    enabled_tools TEXT,
    budget_cents  INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS messages (
    id             TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES sessions(id),
    role           TEXT NOT NULL,
    content        TEXT,
    tool_call_json TEXT,
    tokens_in      INTEGER NOT NULL DEFAULT 0,
    tokens_out     INTEGER NOT NULL DEFAULT 0,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);

CREATE TABLE IF NOT EXISTS tool_executions (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id),
    message_id   TEXT REFERENCES messages(id),
    cluster_id   TEXT REFERENCES clusters(id),
    tool_name    TEXT NOT NULL,
    args_json    TEXT NOT NULL,
    diff_json    TEXT,
    risk         TEXT NOT NULL,
    approved_by  TEXT,
    dry_run      INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL,
    rollback_ref TEXT,
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_toolexec_session ON tool_executions(session_id, created_at);

CREATE TABLE IF NOT EXISTS approvals (
    id            TEXT PRIMARY KEY,
    tool_exec_id  TEXT NOT NULL REFERENCES tool_executions(id),
    approver      TEXT NOT NULL,
    decision      TEXT NOT NULL,
    reason        TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_log (
    id           BIGSERIAL PRIMARY KEY,
    ts           TIMESTAMPTZ NOT NULL DEFAULT now(),
    actor        TEXT NOT NULL,
    action       TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    hash_prev    TEXT NOT NULL,
    hash_curr    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_findings (
    id              TEXT PRIMARY KEY,
    cluster_id      TEXT REFERENCES clusters(id),
    dedup_key       TEXT NOT NULL UNIQUE,
    category        TEXT NOT NULL,
    rule_id         TEXT NOT NULL,
    severity        TEXT NOT NULL,
    resource_ref    TEXT NOT NULL,
    title           TEXT NOT NULL,
    impact          TEXT,
    root_cause      TEXT,
    remediation_json TEXT,
    autofixable     INTEGER NOT NULL DEFAULT 0,
    est_savings_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'open',
    first_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_findings_sev ON audit_findings(severity, status);

CREATE TABLE IF NOT EXISTS ai_usage (
    id           TEXT PRIMARY KEY,
    session_id   TEXT REFERENCES sessions(id),
    provider     TEXT NOT NULL,
    model        TEXT NOT NULL,
    tokens_in    INTEGER NOT NULL DEFAULT 0,
    tokens_out   INTEGER NOT NULL DEFAULT 0,
    cost_cents   DOUBLE PRECISION NOT NULL DEFAULT 0,
    route_reason TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_ai_usage_session ON ai_usage(session_id, created_at);

CREATE TABLE IF NOT EXISTS memories (
    id         TEXT PRIMARY KEY,
    session_id TEXT REFERENCES sessions(id),
    kind       TEXT NOT NULL,
    text       TEXT NOT NULL,
    embedding  BYTEA,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
