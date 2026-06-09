package tools

import (
	"context"
	"strings"
)

// AutoApprover grants approval without a human for low-risk mutation classes the
// operator has explicitly opted into (config: tools.auto_approve). It never
// approves Destructive tools, and it never approves cluster-/fleet-wide changes.
type AutoApprover struct {
	// Classes are the allowed auto-approve labels, e.g. "scale", "rollout-restart".
	Classes []string
	// Fallback is invoked when auto-approval does not apply (typically the TUI).
	Fallback Approver
}

// Approve implements Approver.
func (a *AutoApprover) Approve(ctx context.Context, req ApprovalRequest) (bool, string, error) {
	if req.Risk < Destructive && req.BlastRadius == "namespace" && a.matches(req.Tool) {
		return true, "auto-approver", nil
	}
	if a.Fallback != nil {
		return a.Fallback.Approve(ctx, req)
	}
	return false, "", ErrNotApproved
}

func (a *AutoApprover) matches(tool string) bool {
	for _, c := range a.Classes {
		if strings.Contains(tool, c) {
			return true
		}
	}
	return false
}

// DenyAll is the safe default Approver used when no interactive approver is wired
// (e.g. non-interactive CI without --yes). It rejects every mutation.
type DenyAll struct{}

// Approve implements Approver.
func (DenyAll) Approve(context.Context, ApprovalRequest) (bool, string, error) {
	return false, "", ErrNotApproved
}
