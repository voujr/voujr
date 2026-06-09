package audit

import (
	"context"
	"sort"
	"sync"

	"github.com/voujr/voujr/internal/k8s"
)

// Rule evaluates a cluster Snapshot and returns any findings. Rules must be pure
// (no side effects, no live API calls) so a scan over a snapshot is deterministic
// and parallelizable.
type Rule interface {
	// ID is stable, dotted: "<category>.<name>", e.g. "reliability.missing_readiness".
	ID() string
	Category() Category
	Evaluate(ctx context.Context, snap *k8s.Snapshot) []Finding
}

// RuleSet is a registry of audit rules.
type RuleSet struct {
	mu    sync.RWMutex
	rules map[string]Rule
}

// NewRuleSet creates an empty rule set.
func NewRuleSet() *RuleSet { return &RuleSet{rules: map[string]Rule{}} }

// Register adds a rule.
func (rs *RuleSet) Register(r Rule) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.rules[r.ID()] = r
}

// Enabled returns the active rules minus the disabled IDs, in stable order.
func (rs *RuleSet) Enabled(disabled []string) []Rule {
	skip := map[string]bool{}
	for _, d := range disabled {
		skip[d] = true
	}
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	out := make([]Rule, 0, len(rs.rules))
	for id, r := range rs.rules {
		if !skip[id] {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out
}
