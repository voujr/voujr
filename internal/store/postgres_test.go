package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"

	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/security"
	"github.com/voujr/voujr/internal/session"
	"github.com/voujr/voujr/internal/tools"
)

// openTestPostgres provisions an ephemeral Postgres for the test, or skips if it
// can't be provisioned (no network to fetch the binary, unsupported platform).
func openTestPostgres(t *testing.T) *Postgres {
	t.Helper()
	const port = 54329
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Port(port).
		Database("voujr_test").
		Username("voujr").
		Password("voujr").
		RuntimePath(t.TempDir()))

	if err := pg.Start(); err != nil {
		t.Skipf("embedded postgres unavailable: %v", err)
	}
	t.Cleanup(func() { _ = pg.Stop() })

	dsn := "postgres://voujr:voujr@localhost:54329/voujr_test?sslmode=disable"
	st, err := OpenPostgres(dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestPostgresStore(t *testing.T) {
	st := openTestPostgres(t)
	ctx := context.Background()

	userID, err := st.EnsureUser(ctx, "tester", "", "admin")
	if err != nil {
		t.Fatal(err)
	}
	clusterID, err := st.UpsertCluster(ctx, "prod", "prod-ctx", "eks")
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := st.CreateSession(ctx, userID, clusterID, "read-only", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("message round-trip with tool call", func(t *testing.T) {
		in := []ai.Message{
			{Role: ai.RoleUser, Content: "why are pods restarting?"},
			{Role: ai.RoleAssistant, Content: "checking", ToolCalls: []ai.ToolCall{
				{ID: "c1", Name: "kubectl_get_pods", Args: []byte(`{"namespace":"prod"}`)},
			}},
			{Role: ai.RoleTool, ToolCallID: "c1", Name: "kubectl_get_pods", Content: "12 pods"},
		}
		for _, m := range in {
			if err := st.AppendMessage(ctx, sessionID, m); err != nil {
				t.Fatal(err)
			}
		}
		got, err := st.LoadMessages(ctx, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 || got[0].Content != in[0].Content {
			t.Fatalf("round-trip mismatch: %+v", got)
		}
		if len(got[1].ToolCalls) != 1 || string(got[1].ToolCalls[0].Args) != `{"namespace":"prod"}` {
			t.Fatalf("tool call not preserved: %+v", got[1])
		}
	})

	t.Run("audit hash chain + tamper detection", func(t *testing.T) {
		for _, status := range []string{"ok", "ok", "rejected"} {
			if err := st.Record(ctx, tools.AuditEntry{
				SessionID: sessionID, Tool: "kubectl_scale_deployment", Cluster: "prod",
				Risk: tools.Mutate, Args: []byte(`{"replicas":3}`), Approver: "operator", Status: status,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if broken, err := st.VerifyAuditChain(ctx); err != nil || broken != 0 {
			t.Fatalf("expected intact chain, got brokenAt=%d err=%v", broken, err)
		}
		if _, err := st.db.ExecContext(ctx,
			`UPDATE audit_log SET payload_json='tampered' WHERE id=2`); err != nil {
			t.Fatal(err)
		}
		if broken, _ := st.VerifyAuditChain(ctx); broken != 2 {
			t.Fatalf("tamper should break at id=2, got %d", broken)
		}
	})

	t.Run("finding upsert dedup", func(t *testing.T) {
		f := audit.Finding{
			RuleID: "reliability.missing_readiness_probe", Category: audit.Reliability, Severity: audit.P2,
			Resource: audit.ResourceRef{Cluster: "prod", Namespace: "prod", Kind: "Deployment", Name: "api"},
			Title:    "missing readiness probe",
		}
		if err := st.SaveFinding(ctx, clusterID, f); err != nil {
			t.Fatal(err)
		}
		f.Severity = audit.P1
		if err := st.SaveFinding(ctx, clusterID, f); err != nil {
			t.Fatal(err)
		}
		n, err := st.OpenFindingCount(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("dedup failed: want 1 finding, got %d", n)
		}
	})

	t.Run("memory recall by cosine", func(t *testing.T) {
		save := func(text string, v []float32) {
			if err := st.SaveMemory(ctx, session.Memory{SessionID: sessionID, Kind: "decision", Text: text, Embedding: v}); err != nil {
				t.Fatal(err)
			}
		}
		save("about networking", []float32{1, 0, 0})
		save("about storage", []float32{0, 1, 0})
		got, err := st.RecallMemories(ctx, "", []float32{0.9, 0.1, 0}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Text != "about networking" {
			t.Fatalf("nearest memory wrong: %+v", got)
		}
	})

	t.Run("encryption at rest", func(t *testing.T) {
		cipher, err := security.NewCipher(security.DeriveKey("pg-test-key"))
		if err != nil {
			t.Fatal(err)
		}
		st.SetCipher(cipher)
		defer st.SetCipher(nil) // don't affect other subtests' raw assertions

		sess, err := st.CreateSession(ctx, userID, clusterID, "read-only", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		secret := "password is hunter2"
		if err := st.AppendMessage(ctx, sess, ai.Message{Role: ai.RoleUser, Content: secret}); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := st.db.QueryRowContext(ctx, `SELECT content FROM messages WHERE session_id=$1`, sess).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "hunter2") || !strings.HasPrefix(raw, "enc:") {
			t.Fatalf("content not encrypted on disk: %s", raw)
		}
		msgs, err := st.LoadMessages(ctx, sess)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].Content != secret {
			t.Fatalf("decrypt round-trip failed: %+v", msgs)
		}
	})

	// Unknown session surfaces the shared sentinel.
	if _, err := st.GetSession(ctx, "nope"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("want ErrSessionNotFound, got %v", err)
	}
}
