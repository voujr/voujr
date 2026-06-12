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
	_ "github.com/jackc/pgx/v5/stdlib" // pure-Go Postgres driver, registered as "pgx"

	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/session"
	"github.com/voujr/voujr/internal/tools"
	"github.com/voujr/voujr/migrations"
)

// Postgres is the server/controller persistence backend. It satisfies the same
// store.Store contract as SQLite, so the in-cluster controller and team CLI can
// share a database. Embeddings are stored as BYTEA with brute-force cosine in Go
// (no pgvector extension required); swap to a vector column for large scale.
type Postgres struct {
	db     *sql.DB
	mu     sync.Mutex // serializes audit-log hash-chain appends
	cipher Encryptor
}

var _ Store = (*Postgres)(nil)

// OpenPostgres connects to the given DSN and applies pending migrations.
func OpenPostgres(dsn string) (*Postgres, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	p := &Postgres{db: db}
	if err := p.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return p, nil
}

func (p *Postgres) Close() error          { return p.db.Close() }
func (p *Postgres) SetCipher(e Encryptor) { p.cipher = e }

func (p *Postgres) enc(s string) (string, error) {
	if p.cipher == nil {
		return s, nil
	}
	return p.cipher.Encrypt(s)
}
func (p *Postgres) dec(s string) (string, error) {
	if p.cipher == nil {
		return s, nil
	}
	return p.cipher.Decrypt(s)
}

func (p *Postgres) migrate(ctx context.Context) error {
	if _, err := p.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations(
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrations.FS, "postgres")
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
		if err := p.db.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM schema_migrations WHERE version=$1`, name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		body, err := migrations.FS.ReadFile("postgres/" + name)
		if err != nil {
			return err
		}
		// pgx's extended protocol rejects multi-statement Exec, so split first.
		for _, stmt := range splitSQL(string(body)) {
			if _, err := p.db.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("apply migration %s: %w", name, err)
			}
		}
		if _, err := p.db.ExecContext(ctx,
			`INSERT INTO schema_migrations(version) VALUES($1)`, name); err != nil {
			return err
		}
	}
	return nil
}

// splitSQL breaks a migration into individual statements. Comment (`--`) and
// blank lines are stripped FIRST — before splitting on ';' — so a semicolon
// inside a comment can't split a statement.
func splitSQL(s string) []string {
	var clean strings.Builder
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		clean.WriteString(line)
		clean.WriteByte('\n')
	}
	var out []string
	for _, raw := range strings.Split(clean.String(), ";") {
		if strings.TrimSpace(raw) != "" {
			out = append(out, raw)
		}
	}
	return out
}

// --- identity / session bootstrap ----------------------------------------

func (p *Postgres) EnsureUser(ctx context.Context, subject, email, role string) (string, error) {
	var id string
	err := p.db.QueryRowContext(ctx, `SELECT id FROM users WHERE subject=$1`, subject).Scan(&id)
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
	_, err = p.db.ExecContext(ctx,
		`INSERT INTO users(id,subject,email,role) VALUES($1,$2,$3,$4)`,
		id, subject, nullStr(email), role)
	return id, err
}

func (p *Postgres) UpsertCluster(ctx context.Context, name, kubeContext, provider string) (string, error) {
	var id string
	err := p.db.QueryRowContext(ctx, `SELECT id FROM clusters WHERE name=$1`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = uuid.NewString()
	_, err = p.db.ExecContext(ctx,
		`INSERT INTO clusters(id,name,context,provider) VALUES($1,$2,$3,$4)`,
		id, name, nullStr(kubeContext), nullStr(provider))
	return id, err
}

func (p *Postgres) CreateSession(ctx context.Context, userID, clusterID, mode string, enabledTools []string, budgetCents int) (string, error) {
	id := uuid.NewString()
	var toolsJSON sql.NullString
	if len(enabledTools) > 0 {
		b, _ := json.Marshal(enabledTools)
		toolsJSON = sql.NullString{String: string(b), Valid: true}
	}
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO sessions(id,user_id,cluster_id,mode,enabled_tools,budget_cents) VALUES($1,$2,$3,$4,$5,$6)`,
		id, nullStr(userID), nullStr(clusterID), mode, toolsJSON, budgetCents)
	return id, err
}

func (p *Postgres) GetSession(ctx context.Context, id string) (SessionInfo, error) {
	var (
		info                           SessionInfo
		clusterID, name, kctx, enabled sql.NullString
	)
	err := p.db.QueryRowContext(ctx,
		`SELECT s.id, s.mode, s.cluster_id, c.name, c.context, s.enabled_tools, s.budget_cents
		 FROM sessions s LEFT JOIN clusters c ON s.cluster_id = c.id
		 WHERE s.id = $1`, id).
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

func (p *Postgres) ListSessions(ctx context.Context, limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := p.db.QueryContext(ctx,
		`SELECT s.id, COALESCE(c.name,''), s.mode, s.created_at::text,
		        (SELECT COUNT(*) FROM messages m WHERE m.session_id = s.id)
		 FROM sessions s LEFT JOIN clusters c ON s.cluster_id = c.id
		 ORDER BY s.created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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

// --- session.Store: messages & memory ------------------------------------

func (p *Postgres) AppendMessage(ctx context.Context, sessionID string, m ai.Message) error {
	var toolJSON sql.NullString
	if len(m.ToolCalls) > 0 || m.ToolCallID != "" {
		b, _ := json.Marshal(messagePayload{ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Name: m.Name})
		toolJSON = sql.NullString{String: string(b), Valid: true}
	}
	content, err := p.enc(m.Content)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx,
		`INSERT INTO messages(id,session_id,role,content,tool_call_json) VALUES($1,$2,$3,$4,$5)`,
		uuid.NewString(), sessionID, string(m.Role), content, toolJSON)
	return err
}

func (p *Postgres) LoadMessages(ctx context.Context, sessionID string) ([]ai.Message, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT role, content, tool_call_json FROM messages
		 WHERE session_id=$1 ORDER BY created_at`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []ai.Message
	for rows.Next() {
		var role string
		var content, toolJSON sql.NullString
		if err := rows.Scan(&role, &content, &toolJSON); err != nil {
			return nil, err
		}
		text, err := p.dec(content.String)
		if err != nil {
			return nil, err
		}
		msg := ai.Message{Role: ai.Role(role), Content: text}
		if toolJSON.Valid {
			var pl messagePayload
			if err := json.Unmarshal([]byte(toolJSON.String), &pl); err == nil {
				msg.ToolCalls = pl.ToolCalls
				msg.ToolCallID = pl.ToolCallID
				msg.Name = pl.Name
			}
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (p *Postgres) SaveMemory(ctx context.Context, m session.Memory) error {
	id := m.ID
	if id == "" {
		id = uuid.NewString()
	}
	text, err := p.enc(m.Text)
	if err != nil {
		return err
	}
	_, err = p.db.ExecContext(ctx,
		`INSERT INTO memories(id,session_id,kind,text,embedding) VALUES($1,$2,$3,$4,$5)`,
		id, nullStr(m.SessionID), m.Kind, text, encodeVector(m.Embedding))
	return err
}

func (p *Postgres) RecallMemories(ctx context.Context, sessionID string, query []float32, k int) ([]session.Memory, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, kind, text, embedding FROM memories
		 WHERE ($1 = '' OR session_id = $1) ORDER BY created_at DESC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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
		if text, err = p.dec(text); err != nil {
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

// --- audit, findings, usage ----------------------------------------------

func (p *Postgres) Record(ctx context.Context, e tools.AuditEntry) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var clusterID sql.NullString
	if e.Cluster != "" {
		var id string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM clusters WHERE name=$1`, e.Cluster).Scan(&id); err == nil {
			clusterID = sql.NullString{String: id, Valid: true}
		}
	}

	args := string(e.Args)
	if args == "" {
		args = "{}"
	}
	encArgs, err := p.enc(args)
	if err != nil {
		return err
	}
	var encDiff sql.NullString
	if e.Diff != "" {
		d, err := p.enc(e.Diff)
		if err != nil {
			return err
		}
		encDiff = sql.NullString{String: d, Valid: true}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tool_executions
		 (id,session_id,cluster_id,tool_name,args_json,diff_json,risk,approved_by,dry_run,status,duration_ms)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		uuid.NewString(), nullStr(e.SessionID), clusterID, e.Tool, encArgs, encDiff,
		e.Risk.String(), nullStr(e.Approver), boolToInt(e.DryRun), e.Status, e.Duration.Milliseconds(),
	); err != nil {
		return fmt.Errorf("insert tool_execution: %w", err)
	}

	var prev string
	switch err := tx.QueryRowContext(ctx,
		`SELECT hash_curr FROM audit_log ORDER BY id DESC LIMIT 1`).Scan(&prev); {
	case errors.Is(err, sql.ErrNoRows):
		prev = ""
	case err != nil:
		return err
	}

	payload, _ := json.Marshal(auditPayload{
		Tool: e.Tool, Cluster: e.Cluster, Risk: e.Risk.String(), Status: e.Status,
		Approver: e.Approver, DryRun: e.DryRun, Summary: e.Summary, Args: json.RawMessage(args),
	})
	sum := sha256.Sum256(append([]byte(prev), payload...))
	cur := hex.EncodeToString(sum[:])
	storedPayload, err := p.enc(string(payload))
	if err != nil {
		return err
	}

	actor := e.Approver
	if actor == "" || actor == "n/a" {
		actor = "agent"
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log(actor,action,payload_json,hash_prev,hash_curr) VALUES($1,$2,$3,$4,$5)`,
		actor, "tool:"+e.Tool, storedPayload, prev, cur); err != nil {
		return err
	}
	return tx.Commit()
}

// VerifyAuditChain recomputes the hash chain over the cleartext payloads.
func (p *Postgres) VerifyAuditChain(ctx context.Context) (brokenAt int64, err error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, payload_json, hash_prev, hash_curr FROM audit_log ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	prev := ""
	for rows.Next() {
		var id int64
		var payload, hashPrev, hashCurr string
		if err := rows.Scan(&id, &payload, &hashPrev, &hashCurr); err != nil {
			return 0, err
		}
		clear, err := p.dec(payload)
		if err != nil {
			return 0, err
		}
		sum := sha256.Sum256(append([]byte(prev), []byte(clear)...))
		if hashPrev != prev || hashCurr != hex.EncodeToString(sum[:]) {
			return id, nil
		}
		prev = hashCurr
	}
	return 0, rows.Err()
}

func (p *Postgres) SaveFinding(ctx context.Context, clusterID string, f audit.Finding) error {
	rem, _ := json.Marshal(f.Remediation)
	res, _ := json.Marshal(f.Resource)
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO audit_findings
		 (id,cluster_id,dedup_key,category,rule_id,severity,resource_ref,title,impact,root_cause,
		  remediation_json,autofixable,est_savings_usd,status,first_seen,last_seen)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'open',now(),now())
		 ON CONFLICT(dedup_key) DO UPDATE SET
		   last_seen=now(),
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

func (p *Postgres) OpenFindingCount(ctx context.Context) (int, error) {
	var n int
	err := p.db.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM audit_findings WHERE status != 'resolved'`).Scan(&n)
	return n, err
}

func (p *Postgres) RecordUsage(ctx context.Context, sessionID, provider string, u ai.Usage, routeReason string) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO ai_usage(id,session_id,provider,model,tokens_in,tokens_out,cost_cents,route_reason)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		uuid.NewString(), nullStr(sessionID), provider, u.Model,
		u.InputTokens, u.OutputTokens, u.CostCents, nullStr(routeReason))
	return err
}
