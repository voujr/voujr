package incident

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/voujr/voujr/internal/audit"
)

func finding(sev audit.Severity) audit.Finding {
	return audit.Finding{
		RuleID:   "reliability.example",
		Category: audit.Reliability,
		Severity: sev,
		Title:    "example",
		Resource: audit.ResourceRef{Cluster: "prod", Namespace: "ns", Kind: "Pod", Name: "x"},
	}
}

func TestNotifySlackHonorsThreshold(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		posts++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := NewNotifier(srv.URL, "", audit.P1, srv.Client())
	fired, err := n.Notify(context.Background(), []audit.Finding{
		finding(audit.P0), finding(audit.P1), finding(audit.P2), finding(audit.P3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fired != 2 || posts != 2 {
		t.Fatalf("expected 2 alerts (P0+P1), got fired=%d posts=%d", fired, posts)
	}
}

func TestNotifyPagerDutyPayload(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	n := NewNotifier("", "routing-key", audit.P1, srv.Client())
	n.pdURLOverride = srv.URL

	fired, err := n.Notify(context.Background(), []audit.Finding{finding(audit.P0)})
	if err != nil {
		t.Fatal(err)
	}
	if fired != 1 {
		t.Fatalf("expected 1 alert, got %d", fired)
	}
	if body["routing_key"] != "routing-key" || body["event_action"] != "trigger" {
		t.Fatalf("unexpected PagerDuty payload: %v", body)
	}
}

func TestNotifyDisabledIsNoOp(t *testing.T) {
	n := NewNotifier("", "", audit.P1, nil)
	if n.Enabled() {
		t.Fatal("notifier with no sinks should be disabled")
	}
	fired, err := n.Notify(context.Background(), []audit.Finding{finding(audit.P0)})
	if err != nil || fired != 0 {
		t.Fatalf("disabled notifier should no-op, got fired=%d err=%v", fired, err)
	}
}
