package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/k8s"
)

// auditEngine is the slice of *audit.Engine this tool needs, kept as an interface
// so the tool stays decoupled from the engine's construction.
type auditEngine interface {
	Scan(ctx context.Context, cluster *k8s.Cluster, namespace string) (audit.Report, error)
}

// AuditScan runs the audit engine over the active cluster and returns prioritized
// findings. It is a read tool: it inspects state and proposes fixes but applies
// nothing (any remediation goes through a separate, gated mutating tool).
type AuditScan struct {
	Clusters clusterSource
	Engine   auditEngine
}

func (AuditScan) Name() string { return "audit_scan" }

func (AuditScan) Description() string {
	return "Audit the active cluster for reliability, security, cost, and optimization " +
		"issues. Returns findings ranked by severity (P0..P3) with impact, root cause, " +
		"and recommended remediation. Use this to answer 'what's wrong / wasteful / " +
		"insecure in my cluster?' before proposing fixes."
}

func (AuditScan) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace": map[string]any{
				"type":        "string",
				"description": "Namespace to scan; empty scans all namespaces.",
			},
		},
	}
}

func (AuditScan) Risk() RiskLevel { return Read }

func (a AuditScan) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	var in struct {
		Namespace string `json:"namespace"`
	}
	_ = json.Unmarshal(args, &in)

	c, err := a.Clusters.Active()
	if err != nil {
		return Result{}, err
	}
	rep, err := a.Engine.Scan(ctx, c, in.Namespace)
	if err != nil {
		return Result{}, err
	}

	counts := rep.BySeverity()
	var b strings.Builder
	fmt.Fprintf(&b, "%d findings in %s — P0=%d P1=%d P2=%d P3=%d (scanned in %s)\n",
		len(rep.Findings), nsLabel(in.Namespace),
		counts[audit.P0], counts[audit.P1], counts[audit.P2], counts[audit.P3],
		rep.Duration.Round(1e6))
	for _, f := range rep.Findings {
		fmt.Fprintf(&b, "[%s] %s %s/%s — %s\n      fix: %s\n",
			f.Severity, f.Category, f.Resource.Namespace, f.Resource.Name,
			f.Title, f.Remediation.Summary)
	}

	return Result{
		Summary: fmt.Sprintf("audit: %d findings (%d P0/P1) in %s",
			len(rep.Findings), counts[audit.P0]+counts[audit.P1], nsLabel(in.Namespace)),
		Data:      rep.Findings,
		ModelView: b.String(),
	}, nil
}
