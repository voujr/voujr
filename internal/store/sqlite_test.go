package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/session"
	"github.com/voujr/voujr/internal/tools"
)

// openTestStore returns a fresh store in a temp dir plus a bootstrapped session.
func openTestStore(t *testing.T) (*SQLite, string, string) {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	ctx := context.Background()
	userID, err := st.EnsureUser(ctx, "tester", "", "admin")
	if err != nil {
		t.Fatalf("user: %v", err)
	}
	clusterID, err := st.UpsertCluster(ctx, "prod", "prod-ctx", "eks")
	if err != nil {
		t.Fatalf("cluster: %v", err)
	}
	sessionID, err := st.CreateSession(ctx, userID, clusterID, "read-only", nil, 0)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	return st, sessionID, clusterID
}

func TestMessageRoundTrip(t *testing.T) {
	st, sessionID, _ := openTestStore(t)
	ctx := context.Background()

	in := []ai.Message{
		{Role: ai.RoleUser, Content: "why are pods restarting?"},
		{Role: ai.RoleAssistant, Content: "let me check", ToolCalls: []ai.ToolCall{
			{ID: "call_1", Name: "kubectl_get_pods", Args: []byte(`{"namespace":"prod"}`)},
		}},
		{Role: ai.RoleTool, ToolCallID: "call_1", Name: "kubectl_get_pods", Content: "12 pods, 3 restarting"},
	}
	for _, m := range in {
		if err := st.AppendMessage(ctx, sessionID, m); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := st.LoadMessages(ctx, sessionID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(in) {
		t.Fatalf("want %d messages, got %d", len(in), len(got))
	}
	if got[0].Role != ai.RoleUser || got[0].Content != in[0].Content {
		t.Fatalf("user message round-trip mismatch: %+v", got[0])
	}
	if len(got[1].ToolCalls) != 1 || got[1].ToolCalls[0].Name != "kubectl_get_pods" {
		t.Fatalf("assistant tool call not preserved: %+v", got[1])
	}
	if string(got[1].ToolCalls[0].Args) != `{"namespace":"prod"}` {
		t.Fatalf("tool call args not preserved: %s", got[1].ToolCalls[0].Args)
	}
	if got[2].Role != ai.RoleTool || got[2].ToolCallID != "call_1" {
		t.Fatalf("tool message linkage lost: %+v", got[2])
	}
}

func TestAuditHashChainIntegrity(t *testing.T) {
	st, sessionID, _ := openTestStore(t)
	ctx := context.Background()

	for i, status := range []string{"ok", "ok", "rejected"} {
		err := st.Record(ctx, tools.AuditEntry{
			When:      time.Now(),
			SessionID: sessionID,
			Tool:      "kubectl_scale_deployment",
			Cluster:   "prod",
			Risk:      tools.Mutate,
			Args:      []byte(`{"namespace":"prod","name":"api","replicas":3}`),
			Approver:  "operator",
			Status:    status,
			Summary:   "scale api",
		})
		if err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	// Three tool_executions rows persisted.
	var execs int
	if err := st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_executions`).Scan(&execs); err != nil {
		t.Fatal(err)
	}
	if execs != 3 {
		t.Fatalf("want 3 tool_executions, got %d", execs)
	}

	// Chain is intact.
	if broken, err := st.VerifyAuditChain(ctx); err != nil || broken != 0 {
		t.Fatalf("expected intact chain, got brokenAt=%d err=%v", broken, err)
	}

	// Tamper with the middle row's payload; the chain must now report a break.
	if _, err := st.db.ExecContext(ctx,
		`UPDATE audit_log SET payload_json='{"tampered":true}' WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	broken, err := st.VerifyAuditChain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if broken != 2 {
		t.Fatalf("tamper should break chain at id=2, got brokenAt=%d", broken)
	}
}

func TestFindingUpsertDedup(t *testing.T) {
	st, _, clusterID := openTestStore(t)
	ctx := context.Background()

	f := audit.Finding{
		RuleID:   "reliability.missing_readiness_probe",
		Category: audit.Reliability,
		Severity: audit.P2,
		Resource: audit.ResourceRef{Cluster: "prod", Namespace: "prod", Kind: "Deployment", Name: "api"},
		Title:    "missing readiness probe",
	}
	if err := st.SaveFinding(ctx, clusterID, f); err != nil {
		t.Fatalf("save 1: %v", err)
	}
	// Same dedup key, escalated severity → updates in place, not a duplicate.
	f.Severity = audit.P1
	if err := st.SaveFinding(ctx, clusterID, f); err != nil {
		t.Fatalf("save 2: %v", err)
	}

	n, err := st.OpenFindingCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dedup failed: want 1 open finding, got %d", n)
	}
	var sev string
	if err := st.db.QueryRowContext(ctx,
		`SELECT severity FROM audit_findings WHERE dedup_key=?`, f.DedupKey()).Scan(&sev); err != nil {
		t.Fatal(err)
	}
	if sev != "P1" {
		t.Fatalf("upsert should escalate severity to P1, got %s", sev)
	}
}

func TestMemoryRecallOrdersByCosine(t *testing.T) {
	st, sessionID, _ := openTestStore(t)
	ctx := context.Background()

	save := func(text string, vec []float32) {
		if err := st.SaveMemory(ctx, session.Memory{SessionID: sessionID, Kind: "decision", Text: text, Embedding: vec}); err != nil {
			t.Fatalf("save memory: %v", err)
		}
	}
	save("about networking", []float32{1, 0, 0})
	save("about storage", []float32{0, 1, 0})
	save("about scaling", []float32{0, 0, 1})

	// Query closest to the "networking" vector.
	got, err := st.RecallMemories(ctx, sessionID, []float32{0.9, 0.1, 0}, 2)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want top-2, got %d", len(got))
	}
	if got[0].Text != "about networking" {
		t.Fatalf("nearest memory should rank first, got %q", got[0].Text)
	}
}
