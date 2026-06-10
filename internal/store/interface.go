package store

import (
	"context"

	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/session"
	"github.com/voujr/voujr/internal/tools"
)

// Store is the full persistence contract the application depends on. It is the
// seam a Postgres backend implements (Phase 9b) so the in-cluster controller can
// run against a shared database without touching call sites. *SQLite satisfies
// it; the compile-time assertion below pins the contract.
//
// It composes the narrower consumer interfaces (session.Store, tools.AuditSink)
// plus the controller's FindingSink and the CLI's session/usage operations.
type Store interface {
	// identity & sessions
	EnsureUser(ctx context.Context, subject, email, role string) (string, error)
	UpsertCluster(ctx context.Context, name, kubeContext, provider string) (string, error)
	CreateSession(ctx context.Context, userID, clusterID, mode string, enabledTools []string, budgetCents int) (string, error)
	GetSession(ctx context.Context, id string) (SessionInfo, error)
	ListSessions(ctx context.Context, limit int) ([]SessionSummary, error)

	// conversation messages & long-term memory (session.Store)
	AppendMessage(ctx context.Context, sessionID string, m ai.Message) error
	LoadMessages(ctx context.Context, sessionID string) ([]ai.Message, error)
	SaveMemory(ctx context.Context, m session.Memory) error
	RecallMemories(ctx context.Context, sessionID string, query []float32, k int) ([]session.Memory, error)

	// audit, findings, usage (tools.AuditSink + controller.FindingSink + accounting)
	Record(ctx context.Context, e tools.AuditEntry) error
	SaveFinding(ctx context.Context, clusterID string, f audit.Finding) error
	OpenFindingCount(ctx context.Context) (int, error)
	RecordUsage(ctx context.Context, sessionID, provider string, u ai.Usage, routeReason string) error

	// lifecycle
	SetCipher(e Encryptor)
	Close() error
}

var _ Store = (*SQLite)(nil)
