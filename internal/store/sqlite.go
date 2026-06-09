package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
	_ "modernc.org/sqlite" // pure-Go SQLite driver, registered as "sqlite"

	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/session"
	"github.com/voujr/voujr/internal/tools"
	"github.com/voujr/voujr/migrations"
)

// SQLite is the local persistence backend. It satisfies session.Store and
// tools.AuditSink. It is safe for concurrent use: the pool is capped to a single
// connection so writes serialize, and the audit hash-chain read-then-append is
// additionally guarded by a mutex.
type SQLite struct {
	db *sql.DB
	mu sync.Mutex // serializes audit-log hash-chain appends
}

// Compile-time assertions that SQLite implements the consumer interfaces.
var (
	_ session.Store   = (*SQLite)(nil)
	_ tools.AuditSink = (*SQLite)(nil)
)

// OpenSQLite opens (creating if absent) the database at path and applies all
// pending migrations.
func OpenSQLite(path string) (*SQLite, error) {
	// foreign_keys + a busy timeout via DSN so they apply to every connection.
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite is happiest with a single writer; this also makes the hash chain
	// race-free at the DB layer.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &SQLite{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *SQLite) Close() error { return s.db.Close() }

// migrate is a forward-only runner: it applies every embedded *.sql file whose
// name is not yet recorded in schema_migrations, in lexical order.
func (s *SQLite) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations(
			version    TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var count int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM schema_migrations WHERE version=?`, name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		body, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version) VALUES(?)`, name); err != nil {
			return err
		}
	}
	return nil
}

// --- identity / session bootstrap ----------------------------------------

// EnsureUser returns the id of the user with the given subject, creating it if
// absent. role defaults to "reader".
func (s *SQLite) EnsureUser(ctx context.Context, subject, email, role string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE subject=?`, subject).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if role == "" {
		role = "reader"
	}
	id = uuid.NewString()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO users(id,subject,email,role) VALUES(?,?,?,?)`,
		id, subject, nullStr(email), role)
	return id, err
}

// UpsertCluster returns the id of the named cluster, creating it if absent.
func (s *SQLite) UpsertCluster(ctx context.Context, name, kubeContext, provider string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM clusters WHERE name=?`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = uuid.NewString()
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO clusters(id,name,context,provider) VALUES(?,?,?,?)`,
		id, name, nullStr(kubeContext), nullStr(provider))
	return id, err
}

// CreateSession inserts a new session row and returns its id.
func (s *SQLite) CreateSession(ctx context.Context, userID, clusterID, mode string, enabledTools []string, budgetCents int) (string, error) {
	id := uuid.NewString()
	var toolsJSON sql.NullString
	if len(enabledTools) > 0 {
		b, _ := json.Marshal(enabledTools)
		toolsJSON = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions(id,user_id,cluster_id,mode,enabled_tools,budget_cents) VALUES(?,?,?,?,?,?)`,
		id, nullStr(userID), nullStr(clusterID), mode, toolsJSON, budgetCents)
	return id, err
}

// ErrSessionNotFound is returned by GetSession for an unknown id, so the CLI can
// print a friendly message instead of a raw driver error.
var ErrSessionNotFound = errors.New("session not found")

// SessionInfo is a session's restorable configuration (for --resume).
type SessionInfo struct {
	ID             string
	Mode           string
	ClusterID      string
	ClusterName    string
	ClusterContext string
	EnabledTools   []string
	BudgetCents    int
}

// GetSession returns a session's config for resume. Returns sql.ErrNoRows if the
// id is unknown.
func (s *SQLite) GetSession(ctx context.Context, id string) (SessionInfo, error) {
	var (
		info                           SessionInfo
		clusterID, name, kctx, enabled sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.mode, s.cluster_id, c.name, c.context, s.enabled_tools, s.budget_cents
		 FROM sessions s LEFT JOIN clusters c ON s.cluster_id = c.id
		 WHERE s.id = ?`, id).
		Scan(&info.ID, &info.Mode, &clusterID, &name, &kctx, &enabled, &info.BudgetCents)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionInfo{}, ErrSessionNotFound
	}
	if err != nil {
		return SessionInfo{}, err
	}
	info.ClusterID, info.ClusterName, info.ClusterContext = clusterID.String, name.String, kctx.String
	if enabled.Valid && enabled.String != "" {
		_ = json.Unmarshal([]byte(enabled.String), &info.EnabledTools)
	}
	return info, nil
}

// SessionSummary is a one-line listing of a session for `voujr sessions`.
type SessionSummary struct {
	ID        string
	Cluster   string
	Mode      string
	CreatedAt string
	Messages  int
}

// ListSessions returns the most recent sessions, newest first.
func (s *SQLite) ListSessions(ctx context.Context, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, COALESCE(c.name,''), s.mode, s.created_at,
		        (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)
		 FROM sessions s LEFT JOIN clusters c ON s.cluster_id = c.id
		 ORDER BY s.created_at DESC, s.rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionSummary
	for rows.Next() {
		var ss SessionSummary
		if err := rows.Scan(&ss.ID, &ss.Cluster, &ss.Mode, &ss.CreatedAt, &ss.Messages); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// --- session.Store: conversation messages --------------------------------

// AppendMessage persists one conversation message.
func (s *SQLite) AppendMessage(ctx context.Context, sessionID string, m ai.Message) error {
	var toolJSON sql.NullString
	if len(m.ToolCalls) > 0 || m.ToolCallID != "" {
		payload := messagePayload{ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Name: m.Name}
		b, _ := json.Marshal(payload)
		toolJSON = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO messages(id,session_id,role,content,tool_call_json) VALUES(?,?,?,?,?)`,
		uuid.NewString(), sessionID, string(m.Role), m.Content, toolJSON)
	return err
}

// LoadMessages returns a session's messages in insertion order (for --resume).
func (s *SQLite) LoadMessages(ctx context.Context, sessionID string) ([]ai.Message, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT role, content, tool_call_json FROM messages
		 WHERE session_id=? ORDER BY created_at, rowid`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ai.Message
	for rows.Next() {
		var role, content string
		var toolJSON sql.NullString
		if err := rows.Scan(&role, &content, &toolJSON); err != nil {
			return nil, err
		}
		m := ai.Message{Role: ai.Role(role), Content: content}
		if toolJSON.Valid {
			var p messagePayload
			if err := json.Unmarshal([]byte(toolJSON.String), &p); err == nil {
				m.ToolCalls = p.ToolCalls
				m.ToolCallID = p.ToolCallID
				m.Name = p.Name
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

type messagePayload struct {
	ToolCalls  []ai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

// --- session.Store: long-term memory -------------------------------------

// SaveMemory persists a recallable memory and its embedding.
func (s *SQLite) SaveMemory(ctx context.Context, m session.Memory) error {
	id := m.ID
	if id == "" {
		id = uuid.NewString()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memories(id,session_id,kind,text,embedding) VALUES(?,?,?,?,?)`,
		id, nullStr(m.SessionID), m.Kind, m.Text, encodeVector(m.Embedding))
	return err
}

// RecallMemories returns the top-k memories for a session by cosine similarity to
// query. With an empty query it returns the k most recent.
func (s *SQLite) RecallMemories(ctx context.Context, sessionID string, query []float32, k int) ([]session.Memory, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, text, embedding FROM memories
		 WHERE session_id=? ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		m     session.Memory
		score float64
	}
	var all []scored
	for rows.Next() {
		var id, kind, text string
		var emb []byte
		if err := rows.Scan(&id, &kind, &text, &emb); err != nil {
			return nil, err
		}
		mem := session.Memory{ID: id, SessionID: sessionID, Kind: kind, Text: text, Embedding: decodeVector(emb)}
		sc := 0.0
		if len(query) > 0 {
			sc = cosine(query, mem.Embedding)
		}
		all = append(all, scored{mem, sc})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(query) > 0 {
		sort.SliceStable(all, func(i, j int) bool { return all[i].score > all[j].score })
	}
	if k > 0 && len(all) > k {
		all = all[:k]
	}
	out := make([]session.Memory, len(all))
	for i, s := range all {
		out[i] = s.m
	}
	return out, nil
}

// --- tools.AuditSink: tool_executions + hash-chained audit_log ------------

// Record persists a tool execution and appends a tamper-evident audit-log entry,
// atomically. hash_curr = SHA-256(hash_prev || payload); any edit/deletion breaks
// the chain.
func (s *SQLite) Record(ctx context.Context, e tools.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Resolve cluster id by name (nullable — a finding from an unregistered
	// cluster still records, just without the FK).
	var clusterID sql.NullString
	if e.Cluster != "" {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM clusters WHERE name=?`, e.Cluster).Scan(&id); err == nil {
			clusterID = sql.NullString{String: id, Valid: true}
		}
	}

	args := string(e.Args)
	if args == "" {
		args = "{}"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tool_executions
		 (id,session_id,cluster_id,tool_name,args_json,diff_json,risk,approved_by,dry_run,status,duration_ms)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		uuid.NewString(), nullStr(e.SessionID), clusterID, e.Tool, args, nullStr(e.Diff),
		e.Risk.String(), nullStr(e.Approver), boolToInt(e.DryRun), e.Status, e.Duration.Milliseconds(),
	); err != nil {
		return fmt.Errorf("insert tool_execution: %w", err)
	}

	// Append to the hash chain.
	var prev string
	switch err := tx.QueryRowContext(ctx,
		`SELECT hash_curr FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev); {
	case errors.Is(err, sql.ErrNoRows):
		prev = "" // genesis
	case err != nil:
		return err
	}

	payload, _ := json.Marshal(auditPayload{
		Tool: e.Tool, Cluster: e.Cluster, Risk: e.Risk.String(), Status: e.Status,
		Approver: e.Approver, DryRun: e.DryRun, Summary: e.Summary, Args: json.RawMessage(args),
	})
	sum := sha256.Sum256(append([]byte(prev), payload...))
	cur := hex.EncodeToString(sum[:])

	actor := e.Approver
	if actor == "" || actor == "n/a" {
		actor = "agent"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log(actor,action,payload_json,hash_prev,hash_curr) VALUES(?,?,?,?,?)`,
		actor, "tool:"+e.Tool, string(payload), prev, cur); err != nil {
		return err
	}

	return tx.Commit()
}

type auditPayload struct {
	Tool     string          `json:"tool"`
	Cluster  string          `json:"cluster"`
	Risk     string          `json:"risk"`
	Status   string          `json:"status"`
	Approver string          `json:"approver"`
	DryRun   bool            `json:"dry_run"`
	Summary  string          `json:"summary"`
	Args     json.RawMessage `json:"args"`
}

// VerifyAuditChain recomputes the hash chain and returns the first id where it
// breaks, or 0 if intact. This is the integrity check for the compliance log.
func (s *SQLite) VerifyAuditChain(ctx context.Context) (brokenAt int64, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, payload_json, hash_prev, hash_curr FROM audit_log ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	prev := ""
	for rows.Next() {
		var id int64
		var payload, hashPrev, hashCurr string
		if err := rows.Scan(&id, &payload, &hashPrev, &hashCurr); err != nil {
			return 0, err
		}
		sum := sha256.Sum256(append([]byte(prev), []byte(payload)...))
		want := hex.EncodeToString(sum[:])
		if hashPrev != prev || hashCurr != want {
			return id, nil
		}
		prev = hashCurr
	}
	return 0, rows.Err()
}

// --- audit findings -------------------------------------------------------

// SaveFinding upserts a finding keyed by its dedup key, tracking first/last seen
// so a recurring issue updates in place instead of duplicating each scan.
func (s *SQLite) SaveFinding(ctx context.Context, clusterID string, f audit.Finding) error {
	rem, _ := json.Marshal(f.Remediation)
	res, _ := json.Marshal(f.Resource)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_findings
		 (id,cluster_id,dedup_key,category,rule_id,severity,resource_ref,title,impact,root_cause,
		  remediation_json,autofixable,est_savings_usd,status,first_seen,last_seen)
		 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,'open',datetime('now'),datetime('now'))
		 ON CONFLICT(dedup_key) DO UPDATE SET
		   last_seen=datetime('now'),
		   severity=excluded.severity,
		   title=excluded.title,
		   impact=excluded.impact,
		   root_cause=excluded.root_cause,
		   remediation_json=excluded.remediation_json`,
		uuid.NewString(), nullStr(clusterID), f.DedupKey(), string(f.Category), f.RuleID,
		string(f.Severity), string(res), f.Title, f.Impact, f.RootCause, string(rem),
		boolToInt(f.Autofixable()), f.EstMonthlySavingsUSD)
	return err
}

// OpenFindingCount returns the number of findings not yet resolved.
func (s *SQLite) OpenFindingCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_findings WHERE status != 'resolved'`).Scan(&n)
	return n, err
}

// --- AI usage -------------------------------------------------------------

// RecordUsage persists per-call token/cost accounting for budget + dashboards.
func (s *SQLite) RecordUsage(ctx context.Context, sessionID, provider string, u ai.Usage, routeReason string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO ai_usage(id,session_id,provider,model,tokens_in,tokens_out,cost_cents,route_reason)
		 VALUES(?,?,?,?,?,?,?,?)`,
		uuid.NewString(), nullStr(sessionID), provider, u.Model,
		u.InputTokens, u.OutputTokens, u.CostCents, nullStr(routeReason))
	return err
}
