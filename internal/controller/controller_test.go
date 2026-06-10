package controller

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/voujr/voujr/internal/audit"
)

type fakeScanner struct {
	mu      sync.Mutex
	calls   int
	report  audit.Report
	failErr error
}

func (f *fakeScanner) ScanFleet(context.Context, []string, string) (audit.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failErr != nil {
		return audit.Report{}, f.failErr
	}
	return f.report, nil
}
func (f *fakeScanner) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type fakeSink struct {
	saved   []audit.Finding
	upserts int
}

func (s *fakeSink) UpsertCluster(_ context.Context, name, _, _ string) (string, error) {
	s.upserts++
	return "id-" + name, nil
}
func (s *fakeSink) SaveFinding(_ context.Context, _ string, f audit.Finding) error {
	s.saved = append(s.saved, f)
	return nil
}

type fakeAlerter struct{ fired int }

func (a *fakeAlerter) Notify(_ context.Context, fs []audit.Finding) (int, error) {
	for _, f := range fs {
		if f.Severity == audit.P0 || f.Severity == audit.P1 {
			a.fired++
		}
	}
	return a.fired, nil
}

type fakeMetrics struct{ gauges map[string]int }

func (m *fakeMetrics) SetFinding(category, severity string, n int) {
	if m.gauges == nil {
		m.gauges = map[string]int{}
	}
	m.gauges[category+"/"+severity] = n
}

func finding(cluster string, cat audit.Category, sev audit.Severity) audit.Finding {
	return audit.Finding{
		RuleID: string(cat) + ".x", Category: cat, Severity: sev,
		Resource: audit.ResourceRef{Cluster: cluster, Namespace: "ns", Kind: "Deployment", Name: "api"},
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTickPersistsMetersAndAlerts(t *testing.T) {
	scanner := &fakeScanner{report: audit.Report{Findings: []audit.Finding{
		finding("prod", audit.Security, audit.P1),
		finding("prod", audit.Cost, audit.P2),
		finding("prod", audit.Reliability, audit.P0),
	}}}
	sink := &fakeSink{}
	alerter := &fakeAlerter{}
	metrics := &fakeMetrics{}

	c := New(Config{Scanner: scanner, Sink: sink, Alerter: alerter, Metrics: metrics, Log: quietLogger()})
	c.tick(context.Background())

	if len(sink.saved) != 3 {
		t.Fatalf("expected 3 findings persisted, got %d", len(sink.saved))
	}
	if sink.upserts != 1 {
		t.Fatalf("cluster id should be cached (1 upsert), got %d", sink.upserts)
	}
	if alerter.fired != 2 { // P1 + P0
		t.Fatalf("expected 2 alerts (P0+P1), got %d", alerter.fired)
	}
	// Present buckets reflect counts; absent buckets are reset to 0.
	if metrics.gauges["security/P1"] != 1 || metrics.gauges["reliability/P0"] != 1 {
		t.Fatalf("present gauges wrong: %v", metrics.gauges)
	}
	if metrics.gauges["security/P0"] != 0 {
		t.Fatalf("absent bucket should be 0, got %d", metrics.gauges["security/P0"])
	}
}

func TestTickSurvivesScanError(t *testing.T) {
	scanner := &fakeScanner{failErr: errors.New("api down")}
	sink := &fakeSink{}
	c := New(Config{Scanner: scanner, Sink: sink, Metrics: &fakeMetrics{}, Log: quietLogger()})
	c.tick(context.Background()) // must not panic
	if len(sink.saved) != 0 {
		t.Fatal("no findings should be saved on scan error")
	}
}

func TestRunTicksThenStopsOnCancel(t *testing.T) {
	scanner := &fakeScanner{}
	c := New(Config{
		Scanner: scanner, Sink: &fakeSink{}, Metrics: &fakeMetrics{},
		Log: quietLogger(), Interval: time.Hour, // long, so only the initial tick fires
	})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()

	// Give the initial tick time to run, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
	if scanner.count() < 1 {
		t.Fatal("expected at least the initial tick")
	}
}
