// Package observability wires Prometheus metrics, OpenTelemetry tracing, and
// structured logging. Metrics cover agent performance, tool execution, audit
// findings, AI usage, and cost so the agent's own behavior is measurable.
package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the collectors. One instance is shared across the process.
type Metrics struct {
	ToolExecTotal   *prometheus.CounterVec   // by tool, status
	ToolExecLatency *prometheus.HistogramVec // by tool
	TurnLatency     prometheus.Histogram
	AICostCents     *prometheus.CounterVec // by provider, model
	AITokens        *prometheus.CounterVec // by provider, model, direction
	FindingsTotal   *prometheus.GaugeVec   // by category, severity
	ApprovalsTotal  *prometheus.CounterVec // by decision
}

// NewMetrics registers and returns the collector set.
func NewMetrics() *Metrics {
	return &Metrics{
		ToolExecTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "voujr_tool_executions_total",
			Help: "Tool executions by tool and final status.",
		}, []string{"tool", "status"}),
		ToolExecLatency: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "voujr_tool_execution_seconds",
			Help:    "Tool execution latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"tool"}),
		TurnLatency: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "voujr_turn_seconds",
			Help:    "End-to-end agent turn latency.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60},
		}),
		AICostCents: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "voujr_ai_cost_cents_total",
			Help: "Estimated AI spend in cents by provider/model.",
		}, []string{"provider", "model"}),
		AITokens: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "voujr_ai_tokens_total",
			Help: "AI tokens by provider/model/direction.",
		}, []string{"provider", "model", "direction"}),
		FindingsTotal: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "voujr_audit_findings",
			Help: "Open audit findings by category and severity.",
		}, []string{"category", "severity"}),
		ApprovalsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "voujr_approvals_total",
			Help: "Mutation approval decisions.",
		}, []string{"decision"}),
	}
}

// Serve exposes /metrics on addr (e.g. ":9090"). A no-op when addr is empty.
func Serve(addr string) {
	if addr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	go func() { _ = http.ListenAndServe(addr, mux) }()
}
