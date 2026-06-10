// Package controller is the in-cluster "team mode" runtime: it runs the audit
// engine continuously across the registered fleet, persists findings, updates
// metrics, and fires incidents. It is decoupled from concrete implementations
// via small interfaces so it is straightforward to unit-test a single tick.
package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/voujr/voujr/internal/audit"
)

// Scanner runs an audit across one or more clusters. Satisfied by *audit.Engine.
type Scanner interface {
	ScanFleet(ctx context.Context, clusters []string, namespace string) (audit.Report, error)
}

// FindingSink persists findings. Satisfied by *store.SQLite (and the Postgres
// store once it lands).
type FindingSink interface {
	UpsertCluster(ctx context.Context, name, kubeContext, provider string) (string, error)
	SaveFinding(ctx context.Context, clusterID string, f audit.Finding) error
}

// Alerter escalates qualifying findings. Satisfied by *incident.Notifier.
type Alerter interface {
	Notify(ctx context.Context, findings []audit.Finding) (int, error)
}

// MetricsSink receives the per-bucket open-findings gauge. Satisfied by
// *observability.Metrics.
type MetricsSink interface {
	SetFinding(category, severity string, n int)
}

var (
	allCategories = []audit.Category{audit.Reliability, audit.Security, audit.Cost, audit.Optimization}
	allSeverities = []audit.Severity{audit.P0, audit.P1, audit.P2, audit.P3}
)

// Controller runs the continuous-audit loop.
type Controller struct {
	scanner   Scanner
	sink      FindingSink
	alerter   Alerter // optional
	metrics   MetricsSink
	log       *slog.Logger
	clusters  []string // names; empty = all registered
	namespace string
	interval  time.Duration

	clusterIDs map[string]string // name -> id cache
}

// Config wires a Controller.
type Config struct {
	Scanner   Scanner
	Sink      FindingSink
	Alerter   Alerter
	Metrics   MetricsSink
	Log       *slog.Logger
	Clusters  []string
	Namespace string
	Interval  time.Duration
}

// New builds a Controller, defaulting the interval to 15m.
func New(c Config) *Controller {
	if c.Interval <= 0 {
		c.Interval = 15 * time.Minute
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Controller{
		scanner: c.Scanner, sink: c.Sink, alerter: c.Alerter, metrics: c.Metrics,
		log: c.Log, clusters: c.Clusters, namespace: c.Namespace, interval: c.Interval,
		clusterIDs: map[string]string{},
	}
}

// Run scans once immediately, then every interval, until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	c.log.Info("controller started", "interval", c.interval, "namespace", nsOrAll(c.namespace))
	c.tick(ctx)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			c.log.Info("controller stopping", "reason", ctx.Err())
			return ctx.Err()
		case <-ticker.C:
			c.tick(ctx)
		}
	}
}

// tick performs one scan→persist→meter→alert cycle. Errors are logged, not
// fatal: a transient failure must not kill the loop.
func (c *Controller) tick(ctx context.Context) {
	start := time.Now()
	rep, err := c.scanner.ScanFleet(ctx, c.clusters, c.namespace)
	if err != nil {
		c.log.Error("audit scan failed", "error", err)
		return
	}

	for _, f := range rep.Findings {
		id, err := c.clusterID(ctx, f.Resource.Cluster)
		if err != nil {
			c.log.Warn("resolve cluster failed", "cluster", f.Resource.Cluster, "error", err)
			continue
		}
		if err := c.sink.SaveFinding(ctx, id, f); err != nil {
			c.log.Warn("save finding failed", "rule", f.RuleID, "error", err)
		}
	}

	c.updateGauges(rep.Findings)

	if c.alerter != nil {
		if fired, err := c.alerter.Notify(ctx, rep.Findings); err != nil {
			c.log.Warn("incident notify failed", "error", err)
		} else if fired > 0 {
			c.log.Info("incidents alerted", "count", fired)
		}
	}

	sev := rep.BySeverity()
	c.log.Info("scan complete",
		"findings", len(rep.Findings), "p0", sev[audit.P0], "p1", sev[audit.P1],
		"duration", time.Since(start))
}

// updateGauges sets every category×severity bucket so decreases (resolved
// findings) are reflected, not just increases.
func (c *Controller) updateGauges(findings []audit.Finding) {
	if c.metrics == nil {
		return
	}
	counts := map[[2]string]int{}
	for _, f := range findings {
		counts[[2]string{string(f.Category), string(f.Severity)}]++
	}
	for _, cat := range allCategories {
		for _, s := range allSeverities {
			c.metrics.SetFinding(string(cat), string(s), counts[[2]string{string(cat), string(s)}])
		}
	}
}

func (c *Controller) clusterID(ctx context.Context, name string) (string, error) {
	if id, ok := c.clusterIDs[name]; ok {
		return id, nil
	}
	id, err := c.sink.UpsertCluster(ctx, name, "", "")
	if err != nil {
		return "", err
	}
	c.clusterIDs[name] = id
	return id, nil
}

func nsOrAll(ns string) string {
	if ns == "" {
		return "all"
	}
	return ns
}
