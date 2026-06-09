package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Errors returned by dispatch. Callers (the agent loop) surface these to the
// model as tool results so it can adapt.
var (
	ErrUnknownTool = errors.New("unknown tool")
	ErrNotEnabled  = errors.New("tool not enabled for this session")
	ErrReadOnly    = errors.New("mutating tool blocked: session is read-only")
	ErrDenied      = errors.New("denied by policy")
	ErrNotApproved = errors.New("not approved")
)

// PolicyDecision is the outcome of a policy check.
type PolicyDecision struct {
	Allow           bool
	RequireApproval bool // even in apply mode, force the human gate
	Reason          string
	BlastRadius     string // "namespace" | "cluster" | "fleet"
}

// Policy classifies a proposed call and may deny it outright.
type Policy interface {
	Check(ctx context.Context, t Tool, args RawArgs) (PolicyDecision, error)
}

// ApprovalRequest is presented to a human (or auto-approver) before a mutation.
type ApprovalRequest struct {
	Tool        string
	Cluster     string
	Args        RawArgs
	Diff        string
	Risk        RiskLevel
	BlastRadius string
}

// Approver decides whether a mutation proceeds. The TUI implements this with a
// y/N modal; an auto-approver implements it from policy for low-risk classes.
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (approved bool, approver string, err error)
}

// AuditSink receives an immutable record of every executed call.
type AuditSink interface {
	Record(ctx context.Context, e AuditEntry) error
}

// AuditEntry is one row in the append-only, hash-chained audit log.
type AuditEntry struct {
	When      time.Time
	SessionID string
	Tool      string
	Cluster   string
	Risk      RiskLevel
	Args      RawArgs
	Diff      string
	Approver  string
	DryRun    bool
	Status    string // "ok" | "error" | "denied" | "rejected"
	Summary   string
	Duration  time.Duration
}

// Redactor strips secret-shaped content before it is logged or returned to the
// model.
type Redactor interface {
	Scrub(s string) string
}

// SessionPolicy is the per-session execution context for a dispatch.
type SessionPolicy struct {
	SessionID   string   // persistence key for tool_executions / audit_log
	Mode        string   // "read-only" | "propose" | "apply"
	Enabled     []string // allow-list; empty = all read tools
	Cluster     string
	DryRunFirst bool // always server-dry-run mutations before approval
}

// Registry holds tools and runs the dispatch chain. It is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	tools    map[string]Tool
	policy   Policy
	approver Approver
	audit    AuditSink
	redactor Redactor
}

// NewRegistry builds a registry with its safety collaborators.
func NewRegistry(p Policy, a Approver, audit AuditSink, r Redactor) *Registry {
	return &Registry{
		tools:    map[string]Tool{},
		policy:   p,
		approver: a,
		audit:    audit,
		redactor: r,
	}
}

// Register adds a tool. Duplicate names panic — a programming error.
func (reg *Registry) Register(t Tool) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if _, exists := reg.tools[t.Name()]; exists {
		panic("tools: duplicate registration: " + t.Name())
	}
	reg.tools[t.Name()] = t
}

// Declarations returns model-facing specs for the tools enabled by the session.
// In read-only mode, mutating tools are not advertised at all.
func (reg *Registry) Declarations(sp SessionPolicy) []Declaration {
	reg.mu.RLock()
	defer reg.mu.RUnlock()

	var out []Declaration
	for name, t := range reg.tools {
		if !reg.enabled(sp, name) {
			continue
		}
		if sp.Mode == "read-only" && Mutating(t) {
			continue
		}
		out = append(out, Spec(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (reg *Registry) enabled(sp SessionPolicy, name string) bool {
	if len(sp.Enabled) == 0 {
		return true
	}
	for _, e := range sp.Enabled {
		if e == name {
			return true
		}
	}
	return false
}

// Dispatch runs the full safety chain for one tool call and returns the Result.
// Validation/policy/approval failures are returned as errors; the agent loop
// turns them into tool messages so the model can recover.
func (reg *Registry) Dispatch(ctx context.Context, sp SessionPolicy, name string, args RawArgs) (Result, error) {
	start := time.Now()
	reg.mu.RLock()
	t, ok := reg.tools[name]
	reg.mu.RUnlock()
	if !ok {
		return Result{}, fmt.Errorf("%w: %s", ErrUnknownTool, name)
	}

	// 1. allow-list
	if !reg.enabled(sp, name) {
		return Result{}, fmt.Errorf("%w: %s", ErrNotEnabled, name)
	}

	// 2. schema validation
	if err := validateArgs(t.Schema(), args); err != nil {
		return Result{}, fmt.Errorf("invalid args for %s: %w", name, err)
	}

	mutating := Mutating(t)

	// 3. read-only gate
	if mutating && sp.Mode == "read-only" {
		return Result{}, ErrReadOnly
	}

	// 4. policy / blast radius
	var pd PolicyDecision
	if reg.policy != nil {
		var err error
		pd, err = reg.policy.Check(ctx, t, args)
		if err != nil {
			return Result{}, fmt.Errorf("policy check: %w", err)
		}
		if !pd.Allow {
			reg.record(ctx, t, sp, args, "", "", false, "denied", pd.Reason, time.Since(start))
			return Result{}, fmt.Errorf("%w: %s", ErrDenied, pd.Reason)
		}
	}

	// 5. dry-run to compute a diff for the approval prompt
	var diff string
	if mutating && sp.DryRunFirst {
		dr, err := t.Execute(ctx, args, true)
		if err != nil {
			return Result{}, fmt.Errorf("dry-run failed: %w", err)
		}
		diff = dr.Diff
	}

	// 6. approval (skipped only when policy says allow without approval AND the
	//    session is in apply mode).
	approver := "n/a"
	needApproval := mutating && (sp.Mode != "apply" || pd.RequireApproval)
	if needApproval {
		if reg.approver == nil {
			return Result{}, ErrNotApproved
		}
		ok, who, err := reg.approver.Approve(ctx, ApprovalRequest{
			Tool: name, Cluster: sp.Cluster, Args: args, Diff: diff,
			Risk: t.Risk(), BlastRadius: pd.BlastRadius,
		})
		if err != nil {
			return Result{}, err
		}
		if !ok {
			reg.record(ctx, t, sp, args, diff, who, false, "rejected", "user rejected", time.Since(start))
			return Result{}, ErrNotApproved
		}
		approver = who
	}

	// 7. execute for real
	res, err := t.Execute(ctx, args, false)
	status := "ok"
	if err != nil {
		status = "error"
	}

	// 8. redact model-facing output
	if reg.redactor != nil {
		res.ModelView = reg.redactor.Scrub(res.ModelView)
		res.Summary = reg.redactor.Scrub(res.Summary)
	}

	// 9. audit
	reg.record(ctx, t, sp, args, diff, approver, false, status, res.Summary, time.Since(start))

	return res, err
}

func (reg *Registry) record(ctx context.Context, t Tool, sp SessionPolicy, args RawArgs, diff, approver string, dryRun bool, status, summary string, d time.Duration) {
	if reg.audit == nil {
		return
	}
	_ = reg.audit.Record(ctx, AuditEntry{
		When: time.Now(), SessionID: sp.SessionID, Tool: t.Name(), Cluster: sp.Cluster, Risk: t.Risk(),
		Args: args, Diff: diff, Approver: approver, DryRun: dryRun,
		Status: status, Summary: summary, Duration: d,
	})
}
