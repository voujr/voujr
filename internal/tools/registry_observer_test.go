package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type recObserver struct {
	execs     []string // "tool:status"
	approvals []bool
}

func (r *recObserver) ToolExecuted(tool, status string, _ time.Duration) {
	r.execs = append(r.execs, tool+":"+status)
}
func (r *recObserver) ApprovalDecided(approved bool) { r.approvals = append(r.approvals, approved) }

type yesApprover struct{}

func (yesApprover) Approve(context.Context, ApprovalRequest) (bool, string, error) {
	return true, "tester", nil
}

type noApprover struct{}

func (noApprover) Approve(context.Context, ApprovalRequest) (bool, string, error) {
	return false, "tester", nil
}

func TestObserverRecordsReadExecution(t *testing.T) {
	obs := &recObserver{}
	reg := NewRegistry(nil, nil, nil, nil)
	reg.SetObserver(obs)
	reg.Register(fakeTool{name: "read_thing", risk: Read})

	if _, err := reg.Dispatch(context.Background(),
		SessionPolicy{Mode: "read-only"}, "read_thing", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(obs.execs) != 1 || obs.execs[0] != "read_thing:ok" {
		t.Fatalf("expected one ok execution, got %v", obs.execs)
	}
}

func TestObserverRecordsApprovedMutation(t *testing.T) {
	obs := &recObserver{}
	reg := NewRegistry(nil, yesApprover{}, nil, nil)
	reg.SetObserver(obs)
	reg.Register(fakeTool{name: "mutate_thing", risk: Mutate})

	if _, err := reg.Dispatch(context.Background(),
		SessionPolicy{Mode: "propose"}, "mutate_thing", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if len(obs.approvals) != 1 || !obs.approvals[0] {
		t.Fatalf("expected one approval=true, got %v", obs.approvals)
	}
	if len(obs.execs) != 1 || obs.execs[0] != "mutate_thing:ok" {
		t.Fatalf("expected mutate_thing:ok, got %v", obs.execs)
	}
}

func TestObserverRecordsRejection(t *testing.T) {
	obs := &recObserver{}
	reg := NewRegistry(nil, noApprover{}, nil, nil)
	reg.SetObserver(obs)
	reg.Register(fakeTool{name: "mutate_thing", risk: Mutate})

	_, err := reg.Dispatch(context.Background(),
		SessionPolicy{Mode: "propose"}, "mutate_thing", json.RawMessage(`{}`))
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("want ErrNotApproved, got %v", err)
	}
	if len(obs.approvals) != 1 || obs.approvals[0] {
		t.Fatalf("expected one approval=false, got %v", obs.approvals)
	}
	if len(obs.execs) != 1 || obs.execs[0] != "mutate_thing:rejected" {
		t.Fatalf("expected mutate_thing:rejected, got %v", obs.execs)
	}
}
