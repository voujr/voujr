package audit

import (
	"context"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/voujr/voujr/internal/k8s"
)

// Engine orchestrates scans across clusters and rules.
type Engine struct {
	clusters *k8s.Registry
	rules    *RuleSet
	disabled []string
}

// NewEngine constructs an audit engine.
func NewEngine(clusters *k8s.Registry, rules *RuleSet, disabled []string) *Engine {
	return &Engine{clusters: clusters, rules: rules, disabled: disabled}
}

// Report is the result of a scan.
type Report struct {
	StartedAt time.Time
	Duration  time.Duration
	Findings  []Finding
}

// BySeverity counts findings per severity.
func (r Report) BySeverity() map[Severity]int {
	m := map[Severity]int{}
	for _, f := range r.Findings {
		m[f.Severity]++
	}
	return m
}

// Scan evaluates all enabled rules over a single cluster's snapshot. Rules run
// concurrently since each is a pure function of the snapshot.
func (e *Engine) Scan(ctx context.Context, cluster *k8s.Cluster, namespace string) (Report, error) {
	start := time.Now()
	snap, err := cluster.Snapshot(ctx, namespace)
	if err != nil {
		return Report{}, err
	}

	rules := e.rules.Enabled(e.disabled)
	var (
		mu  sync.Mutex
		all []Finding
	)
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(8)
	for _, r := range rules {
		g.Go(func() error {
			fs := r.Evaluate(gctx, snap)
			if len(fs) > 0 {
				mu.Lock()
				all = append(all, fs...)
				mu.Unlock()
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return Report{}, err
	}

	sortFindings(all)
	return Report{StartedAt: start, Duration: time.Since(start), Findings: all}, nil
}

// ScanFleet fans a scan across many clusters, aggregating findings. Per-cluster
// RBAC is respected because each scan uses that cluster's own handle.
func (e *Engine) ScanFleet(ctx context.Context, clusters []string, namespace string) (Report, error) {
	start := time.Now()
	var (
		mu  sync.Mutex
		all []Finding
	)
	err := e.clusters.FanOut(ctx, clusters, func(ctx context.Context, c *k8s.Cluster) error {
		rep, err := e.Scan(ctx, c, namespace)
		if err != nil {
			return err
		}
		mu.Lock()
		all = append(all, rep.Findings...)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sortFindings(all)
	return Report{StartedAt: start, Duration: time.Since(start), Findings: all}, nil
}

// sortFindings orders by severity (P0 first) then category.
func sortFindings(fs []Finding) {
	rank := map[Severity]int{P0: 0, P1: 1, P2: 2, P3: 3}
	sort.SliceStable(fs, func(i, j int) bool {
		if rank[fs[i].Severity] != rank[fs[j].Severity] {
			return rank[fs[i].Severity] < rank[fs[j].Severity]
		}
		return fs[i].Category < fs[j].Category
	})
}
