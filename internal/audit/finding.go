// Package audit is the cluster audit engine. Rules are pure functions over a
// cluster Snapshot, which makes a scan embarrassingly parallel and easy to test.
// Each rule emits Findings with severity, impact, root cause, remediation, and an
// optional automated fix.
package audit

import "time"

// Severity ranks a finding's urgency.
type Severity string

const (
	P0 Severity = "P0" // active outage / data loss risk
	P1 Severity = "P1" // imminent failure / security exposure
	P2 Severity = "P2" // degradation / significant waste
	P3 Severity = "P3" // hygiene / minor optimization
)

// Category groups findings for reporting and routing.
type Category string

const (
	Reliability  Category = "reliability"
	Security     Category = "security"
	Cost         Category = "cost"
	Optimization Category = "optimization"
)

// ResourceRef identifies the object a finding is about.
type ResourceRef struct {
	Cluster   string
	Namespace string
	Kind      string
	Name      string
}

// Remediation describes how to fix a finding. AutoFix, when non-empty, names a
// tool + args the agent can run (through the approval chain) to apply it.
type Remediation struct {
	Summary           string
	KubectlEquivalent string
	AutoFixTool       string         // e.g. "kubectl_patch_deployment"
	AutoFixArgs       map[string]any // arguments for the tool
}

// Finding is one detected issue.
type Finding struct {
	RuleID      string
	Category    Category
	Severity    Severity
	Resource    ResourceRef
	Title       string
	Impact      string
	RootCause   string
	Remediation Remediation
	// EstMonthlySavingsUSD is populated by cost rules.
	EstMonthlySavingsUSD float64
	DetectedAt           time.Time
}

// Autofixable reports whether the finding carries an executable fix.
func (f Finding) Autofixable() bool { return f.Remediation.AutoFixTool != "" }

// DedupKey identifies a finding across scans so its lifecycle (open → resolved)
// can be tracked rather than duplicated each run.
func (f Finding) DedupKey() string {
	r := f.Resource
	return f.RuleID + "|" + r.Cluster + "|" + r.Namespace + "|" + r.Kind + "|" + r.Name
}
