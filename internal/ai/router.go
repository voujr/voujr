package ai

import (
	"strings"
)

// Complexity is the coarse difficulty class of a request, used for routing.
type Complexity int

const (
	// Trivial: status lookups, formatting, single-resource reads.
	Trivial Complexity = iota
	// Investigation: multi-step reasoning, correlation, remediation planning.
	Investigation
)

// Classified is the router's view of an incoming turn.
type Classified struct {
	Prompt      string
	Complexity  Complexity
	EstTokens   int  // estimated prompt size
	InToolLoop  bool // already mid tool-use; favors a consistent strong model
	BudgetCents int  // remaining session budget; 0 = unlimited
	SpentCents  float64
}

// Decision is the chosen model plus ordered fallbacks.
type Decision struct {
	Routing   Routing
	MaxTokens int
}

// Router maps a classification to a concrete model selection. It is deterministic
// and records a human-readable reason for every decision (stored in ai_usage).
type Router struct {
	// tiers maps "fast"|"reasoning"|"long" to a model ref.
	tiers map[string]string
	// longContextThreshold switches to the long tier above this token estimate.
	longContextThreshold int
}

// NewRouter builds a router from the configured tier map.
func NewRouter(tiers map[string]string) *Router {
	return &Router{tiers: tiers, longContextThreshold: 150_000}
}

// Classify cheaply estimates complexity from surface features. An optional
// tiny-model refinement can be layered on when this is ambiguous.
func Classify(prompt string, estTokens int, inToolLoop bool, budget int, spent float64) Classified {
	c := Classified{
		Prompt: prompt, EstTokens: estTokens, InToolLoop: inToolLoop,
		BudgetCents: budget, SpentCents: spent,
		Complexity: Trivial,
	}
	lower := strings.ToLower(prompt)
	for _, v := range []string{"why", "investigate", "debug", "root cause", "fix", "remediate", "optimize", "audit", "diagnose", "unavailable", "failing"} {
		if strings.Contains(lower, v) {
			c.Complexity = Investigation
			break
		}
	}
	// Once we're iterating over tool results, keep the stronger model for
	// coherent reasoning across the loop.
	if inToolLoop {
		c.Complexity = Investigation
	}
	return c
}

// Route applies policy: long-context need wins first, then complexity, then
// budget pressure can force a downgrade.
func (r *Router) Route(c Classified) Decision {
	tier := "fast"
	reason := "trivial query → fast tier"

	switch {
	case c.EstTokens >= r.longContextThreshold:
		tier, reason = "long", "large context → long-context tier"
	case c.Complexity == Investigation:
		tier, reason = "reasoning", "investigation → strong reasoning tier"
	}

	// Budget pressure: if we've spent >80% of budget, downgrade one step.
	if c.BudgetCents > 0 && c.SpentCents > 0.8*float64(c.BudgetCents) && tier == "reasoning" {
		tier = "fast"
		reason = "budget pressure → downgraded reasoning→fast"
	}

	primary := r.tiers[tier]
	if primary == "" {
		// fall back to any configured tier deterministically
		for _, t := range []string{"reasoning", "long", "fast"} {
			if v := r.tiers[t]; v != "" {
				primary = v
				break
			}
		}
	}

	return Decision{
		Routing: Routing{
			Model:     primary,
			Fallbacks: r.fallbacksFor(tier, primary),
			Reason:    reason,
		},
		MaxTokens: maxTokensFor(tier),
	}
}

// fallbacksFor returns cross-tier fallbacks so a tier outage still yields an answer.
func (r *Router) fallbacksFor(tier, primary string) []string {
	order := map[string][]string{
		"fast":      {"reasoning", "long"},
		"reasoning": {"long", "fast"},
		"long":      {"reasoning", "fast"},
	}
	var fb []string
	for _, t := range order[tier] {
		if v := r.tiers[t]; v != "" && v != primary {
			fb = append(fb, v)
		}
	}
	return fb
}

func maxTokensFor(tier string) int {
	switch tier {
	case "long":
		return 8192
	case "reasoning":
		return 4096
	default:
		return 2048
	}
}
