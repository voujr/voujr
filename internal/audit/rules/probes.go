// Package rules contains concrete audit rules grouped by category. Each rule is
// a pure function over a cluster snapshot.
package rules

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/k8s"
)

// MissingReadinessProbe flags Deployments whose containers lack a readiness
// probe — a common cause of traffic being routed to not-yet-ready pods, leading
// to user-visible 5xx during rollouts.
type MissingReadinessProbe struct{}

func (MissingReadinessProbe) ID() string               { return "reliability.missing_readiness_probe" }
func (MissingReadinessProbe) Category() audit.Category { return audit.Reliability }

func (MissingReadinessProbe) Evaluate(_ context.Context, snap *k8s.Snapshot) []audit.Finding {
	var out []audit.Finding
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		var missing []string
		for _, ctr := range d.Spec.Template.Spec.Containers {
			if ctr.ReadinessProbe == nil {
				missing = append(missing, ctr.Name)
			}
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, audit.Finding{
			RuleID:   "reliability.missing_readiness_probe",
			Category: audit.Reliability,
			Severity: severityForReplicas(d.Status.Replicas),
			Resource: audit.ResourceRef{
				Cluster: snap.Cluster, Namespace: d.Namespace,
				Kind: "Deployment", Name: d.Name,
			},
			Title:     fmt.Sprintf("Deployment %q is missing readiness probes", d.Name),
			Impact:    "Traffic may be routed to pods before they can serve, causing 5xx during rollouts and scale-ups.",
			RootCause: fmt.Sprintf("containers without readinessProbe: %v", missing),
			Remediation: audit.Remediation{
				Summary: "Add a readinessProbe (HTTP/TCP/exec) to each container so the kubelet gates traffic until the pod is ready.",
				KubectlEquivalent: fmt.Sprintf(
					"kubectl -n %s edit deployment/%s  # add spec.template.spec.containers[].readinessProbe",
					d.Namespace, d.Name),
			},
			DetectedAt: timeNow(),
		})
	}
	return out
}

// severityForReplicas escalates the finding when more replicas (and thus more
// production traffic) are exposed.
func severityForReplicas(replicas int32) audit.Severity {
	switch {
	case replicas >= 3:
		return audit.P1
	case replicas >= 1:
		return audit.P2
	default:
		return audit.P3
	}
}

// MissingLivenessProbe flags containers without a liveness probe, which prevents
// the kubelet from restarting wedged-but-not-crashed processes.
type MissingLivenessProbe struct{}

func (MissingLivenessProbe) ID() string               { return "reliability.missing_liveness_probe" }
func (MissingLivenessProbe) Category() audit.Category { return audit.Reliability }

func (MissingLivenessProbe) Evaluate(_ context.Context, snap *k8s.Snapshot) []audit.Finding {
	var out []audit.Finding
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		if !anyContainer(d.Spec.Template.Spec.Containers, func(c corev1.Container) bool {
			return c.LivenessProbe == nil
		}) {
			continue
		}
		out = append(out, audit.Finding{
			RuleID:   "reliability.missing_liveness_probe",
			Category: audit.Reliability,
			Severity: audit.P3,
			Resource: audit.ResourceRef{
				Cluster: snap.Cluster, Namespace: d.Namespace,
				Kind: "Deployment", Name: d.Name,
			},
			Title:     fmt.Sprintf("Deployment %q is missing liveness probes", d.Name),
			Impact:    "Hung processes will not be auto-restarted, prolonging partial outages.",
			RootCause: "one or more containers have no livenessProbe",
			Remediation: audit.Remediation{
				Summary: "Add a livenessProbe so the kubelet can restart unresponsive containers.",
			},
			DetectedAt: timeNow(),
		})
	}
	return out
}

func anyContainer(cs []corev1.Container, pred func(corev1.Container) bool) bool {
	for _, c := range cs {
		if pred(c) {
			return true
		}
	}
	return false
}
