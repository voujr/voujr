-- voujr schema (0001) — portable across SQLite (local) and Postgres (server).
-- Postgres uses pgvector for `memories.embedding`; SQLite stores the vector as a
-- BLOB and does brute-force cosine in Go. Types below use the SQLite spelling;
-- the Postgres migration swaps TEXT timestamps for TIMESTAMPTZ and adds the
-- `vector` column type.

-- Who is operating (server/team mode; a single local user in CLI mode).
CREATE TABLE users (
    id          TEXT PRIMARY KEY,
    subject     TEXT NOT NULL,           -- OIDC subject / OS user
    email       TEXT,
    role        TEXT NOT NULL DEFAULT 'reader', -- reader|operator|approver|admin
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Connected clusters. endpoint_hash avoids storing the raw API server URL.
CREATE TABLE clusters (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL UNIQUE,
    context       TEXT,                  -- kube-context name
    provider      TEXT,                  -- eks|gke|aks|kind|other
    endpoint_hash TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- A conversation against a cluster, with its authority mode and tool allow-list.
CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,
    user_id       TEXT REFERENCES users(id),
    cluster_id    TEXT REFERENCES clusters(id),
    mode          TEXT NOT NULL DEFAULT 'read-only', -- read-only|propose|apply
    enabled_tools TEXT,                  -- JSON array; null = all read tools
    budget_cents  INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Every message in every conversation.
CREATE TABLE messages (
    id             TEXT PRIMARY KEY,
    session_id     TEXT NOT NULL REFERENCES sessions(id),
    role           TEXT NOT NULL,        -- system|user|assistant|tool
    content        TEXT,
    tool_call_json TEXT,                 -- assistant tool calls / tool result linkage
    tokens_in      INTEGER NOT NULL DEFAULT 0,
    tokens_out     INTEGER NOT NULL DEFAULT 0,
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_messages_session ON messages(session_id, created_at);

-- The operational record of every proposed/executed action: the truth source for
-- what the agent did, with its diff, approver, dry-run flag, and rollback handle.
CREATE TABLE tool_executions (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id),
    message_id   TEXT REFERENCES messages(id),
    cluster_id   TEXT REFERENCES clusters(id),
    tool_name    TEXT NOT NULL,
    args_json    TEXT NOT NULL,
    diff_json    TEXT,
    risk         TEXT NOT NULL,          -- read|mutate|destructive
    approved_by  TEXT,
    dry_run      INTEGER NOT NULL DEFAULT 0,
    status       TEXT NOT NULL,          -- ok|error|denied|rejected
    rollback_ref TEXT,                   -- pointer to prior-state snapshot
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_toolexec_session ON tool_executions(session_id, created_at);

-- Human approval decisions (separation of duties: approver may differ from the
-- session user who proposed the action).
CREATE TABLE approvals (
    id            TEXT PRIMARY KEY,
    tool_exec_id  TEXT NOT NULL REFERENCES tool_executions(id),
    approver      TEXT NOT NULL,
    decision      TEXT NOT NULL,         -- approved|rejected
    reason        TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Append-only, hash-chained audit log: the compliance artifact. Each row's
-- hash_prev = H(prev.hash_curr); tampering breaks the chain.
CREATE TABLE audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         TEXT NOT NULL DEFAULT (datetime('now')),
    actor      TEXT NOT NULL,
    action     TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    hash_prev  TEXT NOT NULL,
    hash_curr  TEXT NOT NULL
);

-- Persisted audit findings with lifecycle and dedup across scans.
CREATE TABLE audit_findings (
    id            TEXT PRIMARY KEY,
    cluster_id    TEXT REFERENCES clusters(id),
    dedup_key     TEXT NOT NULL,
    category      TEXT NOT NULL,          -- reliability|security|cost|optimization
    rule_id       TEXT NOT NULL,
    severity      TEXT NOT NULL,          -- P0|P1|P2|P3
    resource_ref  TEXT NOT NULL,          -- JSON {namespace,kind,name}
    title         TEXT NOT NULL,
    impact        TEXT,
    root_cause    TEXT,
    remediation_json TEXT,
    autofixable   INTEGER NOT NULL DEFAULT 0,
    est_savings_usd REAL NOT NULL DEFAULT 0,
    status        TEXT NOT NULL DEFAULT 'open', -- open|acknowledged|resolved
    first_seen    TEXT NOT NULL DEFAULT (datetime('now')),
    last_seen     TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE(dedup_key)
);
CREATE INDEX idx_findings_sev ON audit_findings(severity, status);

-- Per-call AI usage for cost dashboards and budget enforcement.
CREATE TABLE ai_usage (
    id           TEXT PRIMARY KEY,
    session_id   TEXT REFERENCES sessions(id),
    provider     TEXT NOT NULL,
    model        TEXT NOT NULL,
    tokens_in    INTEGER NOT NULL DEFAULT 0,
    tokens_out   INTEGER NOT NULL DEFAULT 0,
    cost_cents   REAL NOT NULL DEFAULT 0,
    route_reason TEXT,
    created_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX idx_ai_usage_session ON ai_usage(session_id, created_at);

-- Long-term recallable memory. In Postgres `embedding` is `vector(N)` with an
-- ivfflat/hnsw index; in SQLite it is a BLOB scanned in Go.
CREATE TABLE memories (
    id         TEXT PRIMARY KEY,
    session_id TEXT REFERENCES sessions(id),
    kind       TEXT NOT NULL,            -- root_cause|preference|cluster_quirk|decision
    text       TEXT NOT NULL,
    embedding  BLOB,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
