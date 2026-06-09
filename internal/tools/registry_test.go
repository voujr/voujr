package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeTool is a minimal Tool for exercising the dispatch chain.
type fakeTool struct {
	name string
	risk RiskLevel
}

func (f fakeTool) Name() string           { return f.name }
func (f fakeTool) Description() string    { return "fake tool" }
func (f fakeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (f fakeTool) Risk() RiskLevel        { return f.risk }
func (f fakeTool) Execute(_ context.Context, _ RawArgs, _ bool) (Result, error) {
	return Result{Summary: "ran " + f.name, ModelView: "ok"}, nil
}

// The read-only gate must block a mutating tool before it can execute — this is
// the core safety invariant.
func TestReadOnlyGateBlocksMutation(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, nil)
	reg.Register(fakeTool{name: "mutate_thing", risk: Mutate})

	_, err := reg.Dispatch(context.Background(),
		SessionPolicy{Mode: "read-only"}, "mutate_thing", json.RawMessage(`{}`))
	if !errors.Is(err, ErrReadOnly) {
		t.Fatalf("expected ErrReadOnly, got %v", err)
	}
}

// A read tool is not advertised-or-gated and runs straight through.
func TestReadToolRuns(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, nil)
	reg.Register(fakeTool{name: "read_thing", risk: Read})

	res, err := reg.Dispatch(context.Background(),
		SessionPolicy{Mode: "read-only"}, "read_thing", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Summary != "ran read_thing" {
		t.Fatalf("got summary %q", res.Summary)
	}
}

// An unknown tool name (e.g. a model hallucination) is rejected, not executed.
func TestUnknownToolRejected(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, nil)
	_, err := reg.Dispatch(context.Background(),
		SessionPolicy{Mode: "read-only"}, "does_not_exist", nil)
	if !errors.Is(err, ErrUnknownTool) {
		t.Fatalf("expected ErrUnknownTool, got %v", err)
	}
}

// Read-only mode must not even advertise mutating tools to the model.
func TestDeclarationsHideMutatingInReadOnly(t *testing.T) {
	reg := NewRegistry(nil, nil, nil, nil)
	reg.Register(fakeTool{name: "read_thing", risk: Read})
	reg.Register(fakeTool{name: "mutate_thing", risk: Mutate})

	decls := reg.Declarations(SessionPolicy{Mode: "read-only"})
	if len(decls) != 1 || decls[0].Name != "read_thing" {
		t.Fatalf("read-only must expose only read tools, got %+v", decls)
	}
}
