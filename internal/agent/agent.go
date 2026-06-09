// Package agent is the runtime: a bounded ReAct loop (reason → act → observe)
// with a hard step budget and a human-in-the-loop gate on every mutation. The
// LLM proposes which tool to call and with what arguments; deterministic code in
// the tools package validates, gates, executes, and audits.
package agent

import (
	"context"

	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/k8s"
	"github.com/voujr/voujr/internal/tools"
)

// EventKind classifies a streamed runtime event for the UI.
type EventKind int

const (
	// EventToken is a streamed text fragment of the assistant's answer.
	EventToken EventKind = iota
	// EventToolStart fires when a tool begins executing.
	EventToolStart
	// EventToolDone fires when a tool finishes (success or error).
	EventToolDone
	// EventRouting reports the model the router selected and why.
	EventRouting
	// EventDone marks the end of a turn.
	EventDone
	// EventError reports a fatal turn error.
	EventError
)

// Event is emitted to the UI as the turn progresses.
type Event struct {
	Kind EventKind
	Text string // token text, tool summary, routing reason, or error
	Tool string
	Err  error
}

// Emit is how the runtime streams events to a consumer (the TUI).
type Emit func(Event)

// Agent holds the collaborators for a session and runs turns.
type Agent struct {
	provider ai.Provider
	router   *ai.Router
	registry *tools.Registry
	clusters *k8s.Registry

	sp       tools.SessionPolicy
	history  []ai.Message
	maxSteps int
	spent    float64 // cumulative cost cents this session
	budget   int

	// persist, if set, durably records each conversation message (user,
	// assistant, tool). It is best-effort: a storage error never aborts a turn.
	persist func(context.Context, ai.Message) error
}

// Config wires an Agent.
type Config struct {
	Provider    ai.Provider
	Router      *ai.Router
	Registry    *tools.Registry
	Clusters    *k8s.Registry
	Session     tools.SessionPolicy
	MaxSteps    int
	BudgetCents int
	// Persist durably records each conversation message; nil disables persistence.
	Persist func(context.Context, ai.Message) error
}

// New builds an Agent seeded with the system prompt.
func New(c Config) *Agent {
	if c.MaxSteps == 0 {
		c.MaxSteps = 12
	}
	a := &Agent{
		provider: c.Provider,
		router:   c.Router,
		registry: c.Registry,
		clusters: c.Clusters,
		sp:       c.Session,
		maxSteps: c.MaxSteps,
		budget:   c.BudgetCents,
		persist:  c.Persist,
	}
	a.history = []ai.Message{{Role: ai.RoleSystem, Content: systemPreamble}}
	return a
}

// record durably saves a message if persistence is configured. Best-effort:
// storage failures are swallowed so a DB hiccup never breaks the conversation.
func (a *Agent) record(ctx context.Context, m ai.Message) {
	if a.persist == nil {
		return
	}
	_ = a.persist(ctx, m)
}

// History returns the conversation so far (for persistence/resume).
func (a *Agent) History() []ai.Message { return a.history }

// Restore rehydrates a prior conversation (resume).
func (a *Agent) Restore(msgs []ai.Message) { a.history = msgs }

// Run executes one user turn, streaming events via emit. It returns the final
// assistant text. The bounded loop lives in loop.go.
func (a *Agent) Run(ctx context.Context, userMsg string, emit Emit) (string, error) {
	return a.runLoop(ctx, userMsg, emit)
}
