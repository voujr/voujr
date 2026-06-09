package ai

import "testing"

func newTestRouter() *Router {
	return NewRouter(map[string]string{"fast": "f", "reasoning": "r", "long": "l"})
}

func TestRouteTrivialPicksFast(t *testing.T) {
	d := newTestRouter().Route(Classify("show me the pods", 100, false, 0, 0))
	if d.Routing.Model != "f" {
		t.Fatalf("trivial query should route to fast, got %q (%s)", d.Routing.Model, d.Routing.Reason)
	}
}

func TestRouteInvestigationPicksReasoning(t *testing.T) {
	d := newTestRouter().Route(Classify("why are pods restarting?", 100, false, 0, 0))
	if d.Routing.Model != "r" {
		t.Fatalf("investigation should route to reasoning, got %q (%s)", d.Routing.Model, d.Routing.Reason)
	}
}

func TestRouteLargeContextPicksLong(t *testing.T) {
	d := newTestRouter().Route(Classify("summarize everything", 200_000, false, 0, 0))
	if d.Routing.Model != "l" {
		t.Fatalf("large context should route to long tier, got %q (%s)", d.Routing.Model, d.Routing.Reason)
	}
}

func TestRouteBudgetPressureDowngrades(t *testing.T) {
	// Investigation would pick reasoning, but 90% of a 100c budget is spent.
	d := newTestRouter().Route(Classify("investigate the outage", 100, false, 100, 90))
	if d.Routing.Model != "f" {
		t.Fatalf("budget pressure should downgrade to fast, got %q (%s)", d.Routing.Model, d.Routing.Reason)
	}
}

func TestRouteFallbacksAreCrossTier(t *testing.T) {
	d := newTestRouter().Route(Classify("why is this failing?", 100, false, 0, 0))
	if len(d.Routing.Fallbacks) == 0 {
		t.Fatal("expected cross-tier fallbacks for resilience")
	}
	for _, fb := range d.Routing.Fallbacks {
		if fb == d.Routing.Model {
			t.Fatalf("fallback %q duplicates primary", fb)
		}
	}
}
