package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MemorySink persists a durable operational fact for future recall.
type MemorySink interface {
	Remember(ctx context.Context, kind, text string) error
}

// Remember lets the agent persist a durable conclusion (a recurring root cause, a
// cluster quirk, an approved remediation, a user preference) so it can be recalled
// in later sessions. It writes only to local memory — no cluster side effect — so
// it carries Read risk and needs no approval.
type Remember struct{ Sink MemorySink }

func (Remember) Name() string { return "remember" }

func (Remember) Description() string {
	return "Persist a durable operational fact for future sessions: a recurring root cause, " +
		"a cluster quirk, an approved remediation, or a user preference. Use sparingly for " +
		"conclusions that stay true — not transient cluster state (which is re-read live)."
}

func (Remember) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"text"},
		"properties": map[string]any{
			"kind": map[string]any{
				"type": "string",
				"enum": []any{"root_cause", "cluster_quirk", "decision", "preference"},
			},
			"text": map[string]any{"type": "string", "description": "the fact to remember, self-contained"},
		},
	}
}

func (Remember) Risk() RiskLevel { return Read }

func (r Remember) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	if r.Sink == nil {
		return Result{}, fmt.Errorf("memory is not configured")
	}
	var in struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Text) == "" {
		return Result{}, fmt.Errorf("text is required")
	}
	if in.Kind == "" {
		in.Kind = "decision"
	}
	if err := r.Sink.Remember(ctx, in.Kind, in.Text); err != nil {
		return Result{}, err
	}
	summary := fmt.Sprintf("remembered (%s): %s", in.Kind, in.Text)
	return Result{Summary: summary, ModelView: summary}, nil
}
