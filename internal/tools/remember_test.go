package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type fakeSink struct {
	called     bool
	kind, text string
}

func (f *fakeSink) Remember(_ context.Context, kind, text string) error {
	f.called, f.kind, f.text = true, kind, text
	return nil
}

func TestRememberPersistsFact(t *testing.T) {
	sink := &fakeSink{}
	tool := Remember{Sink: sink}
	args, _ := json.Marshal(map[string]any{"kind": "root_cause", "text": "api-gateway needs 90s startup budget"})

	res, err := tool.Execute(context.Background(), args, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sink.called || sink.kind != "root_cause" || sink.text != "api-gateway needs 90s startup budget" {
		t.Fatalf("sink not invoked correctly: %+v", sink)
	}
	if !strings.Contains(res.Summary, "remembered") {
		t.Fatalf("unexpected summary: %s", res.Summary)
	}
}

func TestRememberRequiresText(t *testing.T) {
	if _, err := (Remember{Sink: &fakeSink{}}).Execute(
		context.Background(), json.RawMessage(`{"text":"   "}`), false); err == nil {
		t.Fatal("expected an error for empty text")
	}
}

func TestRememberDefaultsKind(t *testing.T) {
	sink := &fakeSink{}
	_, err := (Remember{Sink: sink}).Execute(
		context.Background(), json.RawMessage(`{"text":"prefer blue/green for payments"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if sink.kind != "decision" {
		t.Fatalf("kind should default to 'decision', got %q", sink.kind)
	}
}
