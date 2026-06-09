// Package session manages conversational state across two horizons: a short-term
// rolling window (working memory) and long-term, embedded, recallable memory
// (durable conclusions). Cluster facts are NOT memory — they are re-read live
// each turn; memory holds conclusions and preferences that remain true.
package session

import (
	"context"
	"sort"

	"github.com/voujr/voujr/internal/ai"
)

// Memory is a durable, recallable fact extracted from a conversation.
type Memory struct {
	ID        string
	SessionID string
	Kind      string // "root_cause" | "preference" | "cluster_quirk" | "decision"
	Text      string
	Embedding []float32
}

// Store persists conversations and memories. Implemented by SQLite (local) and
// Postgres (server); see internal/store.
type Store interface {
	AppendMessage(ctx context.Context, sessionID string, m ai.Message) error
	LoadMessages(ctx context.Context, sessionID string) ([]ai.Message, error)
	SaveMemory(ctx context.Context, m Memory) error
	// RecallMemories returns the top-k memories most similar to the query
	// embedding (pgvector in Postgres; brute-force cosine in SQLite).
	RecallMemories(ctx context.Context, sessionID string, query []float32, k int) ([]Memory, error)
}

// Manager coordinates the two memory horizons for one session.
type Manager struct {
	store     Store
	embedder  ai.Provider
	sessionID string

	// window is the verbatim short-term buffer; older turns are summarized.
	window    []ai.Message
	maxWindow int
	summary   string // rolling summary of evicted turns
}

// NewManager builds a session memory manager.
func NewManager(store Store, embedder ai.Provider, sessionID string, maxWindow int) *Manager {
	if maxWindow == 0 {
		maxWindow = 40
	}
	return &Manager{store: store, embedder: embedder, sessionID: sessionID, maxWindow: maxWindow}
}

// Append records a message to the window and to durable storage. When the window
// overflows, the oldest turns are folded into the rolling summary (see
// summarize.go) so the prompt stays within budget without losing the thread.
func (m *Manager) Append(ctx context.Context, msg ai.Message) error {
	m.window = append(m.window, msg)
	if len(m.window) > m.maxWindow {
		evicted := m.window[:len(m.window)-m.maxWindow]
		m.summary = foldSummary(m.summary, evicted)
		m.window = m.window[len(m.window)-m.maxWindow:]
	}
	return m.store.AppendMessage(ctx, m.sessionID, msg)
}

// PromptContext returns the messages to prepend to a turn: the rolling summary (as
// a system note) plus relevant recalled long-term memories.
func (m *Manager) PromptContext(ctx context.Context, userMsg string) ([]ai.Message, error) {
	var out []ai.Message
	if m.summary != "" {
		out = append(out, ai.Message{
			Role:    ai.RoleSystem,
			Content: "[summary of earlier conversation]\n" + m.summary,
		})
	}
	recalled, err := m.recall(ctx, userMsg, 5)
	if err == nil && len(recalled) > 0 {
		var b string
		for _, r := range recalled {
			b += "- " + r.Text + "\n"
		}
		out = append(out, ai.Message{
			Role:    ai.RoleSystem,
			Content: "[recalled operational memory]\n" + b,
		})
	}
	return out, nil
}

// Remember extracts and persists a salient fact for future recall.
func (m *Manager) Remember(ctx context.Context, kind, text string) error {
	embs, err := m.embedder.Embed(ctx, []string{text})
	if err != nil {
		return err
	}
	var emb []float32
	if len(embs) > 0 {
		emb = embs[0]
	}
	return m.store.SaveMemory(ctx, Memory{
		SessionID: m.sessionID, Kind: kind, Text: text, Embedding: emb,
	})
}

func (m *Manager) recall(ctx context.Context, query string, k int) ([]Memory, error) {
	embs, err := m.embedder.Embed(ctx, []string{query})
	if err != nil || len(embs) == 0 {
		return nil, err
	}
	mems, err := m.store.RecallMemories(ctx, m.sessionID, embs[0], k)
	if err != nil {
		return nil, err
	}
	// stable order for deterministic prompts
	sort.Slice(mems, func(i, j int) bool { return mems[i].Text < mems[j].Text })
	return mems, nil
}
