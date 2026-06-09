package rules

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/k8s"
)

// MissingResourceRequests flags containers without CPU or memory requests. Without
// requests the scheduler can't bin-pack or right-size, the workload has no QoS
// guarantee, and the cluster is prone to over-provisioning and noisy neighbors.
type MissingResourceRequests struct{}

func (MissingResourceRequests) ID() string               { return "cost.missing_resource_requests" }
func (MissingResourceRequests) Category() audit.Category { return audit.Cost }

func (MissingResourceRequests) Evaluate(_ context.Context, snap *k8s.Snapshot) []audit.Finding {
	var out []audit.Finding
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		var missing []string
		for _, c := range d.Spec.Template.Spec.Containers {
			if !hasRequest(c, corev1.ResourceCPU) || !hasRequest(c, corev1.ResourceMemory) {
				missing = append(missing, c.Name)
			}
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, audit.Finding{
			RuleID:    "cost.missing_resource_requests",
			Category:  audit.Cost,
			Severity:  audit.P2,
			Resource:  audit.ResourceRef{Cluster: snap.Cluster, Namespace: d.Namespace, Kind: "Deployment", Name: d.Name},
			Title:     fmt.Sprintf("Deployment %q is missing CPU/memory requests", d.Name),
			Impact:    "Without requests the scheduler cannot bin-pack or right-size this workload, driving node over-provisioning and cost.",
			RootCause: fmt.Sprintf("containers without complete requests: %v", missing),
			Remediation: audit.Remediation{
				Summary: "Set resources.requests.cpu and resources.requests.memory based on observed usage (metrics-server / VPA recommendations).",
				KubectlEquivalent: fmt.Sprintf(
					"kubectl -n %s set resources deployment/%s --requests=cpu=100m,memory=128Mi",
					d.Namespace, d.Name),
			},
			DetectedAt: timeNow(),
		})
	}
	return out
}

// MissingMemoryLimit flags containers without a memory limit, which lets a single
// pod consume node memory and trigger OOM kills of co-located workloads.
type MissingMemoryLimit struct{}

func (MissingMemoryLimit) ID() string               { return "optimization.missing_memory_limit" }
func (MissingMemoryLimit) Category() audit.Category { return audit.Optimization }

func (MissingMemoryLimit) Evaluate(_ context.Context, snap *k8s.Snapshot) []audit.Finding {
	var out []audit.Finding
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		var missing []string
		for _, c := range d.Spec.Template.Spec.Containers {
			if !hasLimit(c, corev1.ResourceMemory) {
				missing = append(missing, c.Name)
			}
		}
		if len(missing) == 0 {
			continue
		}
		out = append(out, audit.Finding{
			RuleID:    "optimization.missing_memory_limit",
			Category:  audit.Optimization,
			Severity:  audit.P3,
			Resource:  audit.ResourceRef{Cluster: snap.Cluster, Namespace: d.Namespace, Kind: "Deployment", Name: d.Name},
			Title:     fmt.Sprintf("Deployment %q has no memory limit", d.Name),
			Impact:    "A leak or spike can exhaust node memory and OOM-kill neighbors; the node loses scheduling predictability.",
			RootCause: fmt.Sprintf("containers without memory limit: %v", missing),
			Remediation: audit.Remediation{
				Summary: "Set resources.limits.memory (typically 1.5–2x the request) so the kubelet can cap and isolate the container.",
			},
			DetectedAt: timeNow(),
		})
	}
	return out
}

func hasRequest(c corev1.Container, name corev1.ResourceName) bool {
	if c.Resources.Requests == nil {
		return false
	}
	_, ok := c.Resources.Requests[name]
	return ok
}

func hasLimit(c corev1.Container, name corev1.ResourceName) bool {
	if c.Resources.Limits == nil {
		return false
	}
	_, ok := c.Resources.Limits[name]
	return ok
}
