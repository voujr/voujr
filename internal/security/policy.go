package security

import (
	"context"
	"encoding/json"

	"github.com/voujr/voujr/internal/tools"
)

// Policy classifies a proposed tool call's blast radius and can deny disallowed
// mutations outright. It implements tools.Policy.
//
// The policy is deliberately conservative: anything it cannot scope confidently
// is treated as higher-risk and forced through approval.
type Policy struct {
	// DenyNamespaces blocks all mutations in these namespaces (e.g. kube-system).
	DenyNamespaces map[string]bool
	// ClusterWideRequiresApproval forces approval for cluster-scoped writes even
	// in apply mode.
	ClusterWideRequiresApproval bool
}

// DefaultPolicy returns a sane production default.
func DefaultPolicy() *Policy {
	return &Policy{
		DenyNamespaces:              map[string]bool{"kube-system": true, "kube-node-lease": true},
		ClusterWideRequiresApproval: true,
	}
}

// Check implements tools.Policy.
func (p *Policy) Check(_ context.Context, t tools.Tool, args tools.RawArgs) (tools.PolicyDecision, error) {
	if !tools.Mutating(t) {
		return tools.PolicyDecision{Allow: true, BlastRadius: "namespace"}, nil
	}

	var a struct {
		Namespace string `json:"namespace"`
	}
	_ = json.Unmarshal(args, &a)

	// Hard denies.
	if a.Namespace != "" && p.DenyNamespaces[a.Namespace] {
		return tools.PolicyDecision{
			Allow:  false,
			Reason: "mutations are blocked in protected namespace " + a.Namespace,
		}, nil
	}

	blast := "namespace"
	if a.Namespace == "" {
		blast = "cluster" // no namespace on a mutation ⇒ treat as cluster-scoped
	}

	dec := tools.PolicyDecision{Allow: true, BlastRadius: blast}

	// Destructive always needs approval; cluster-wide too when configured.
	if t.Risk() == tools.Destructive {
		dec.RequireApproval = true
		dec.Reason = "destructive action"
	}
	if blast == "cluster" && p.ClusterWideRequiresApproval {
		dec.RequireApproval = true
		dec.Reason = "cluster-scoped mutation"
	}
	return dec, nil
}
