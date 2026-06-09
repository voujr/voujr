package rules

import (
	"context"
	"fmt"

	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/k8s"
)

// PrivilegedContainer flags any container running privileged — it gets full host
// device access and can trivially escape to the node.
type PrivilegedContainer struct{}

func (PrivilegedContainer) ID() string               { return "security.privileged_container" }
func (PrivilegedContainer) Category() audit.Category { return audit.Security }

func (PrivilegedContainer) Evaluate(_ context.Context, snap *k8s.Snapshot) []audit.Finding {
	var out []audit.Finding
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		var bad []string
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.SecurityContext != nil && c.SecurityContext.Privileged != nil && *c.SecurityContext.Privileged {
				bad = append(bad, c.Name)
			}
		}
		if len(bad) == 0 {
			continue
		}
		out = append(out, audit.Finding{
			RuleID:    "security.privileged_container",
			Category:  audit.Security,
			Severity:  audit.P1,
			Resource:  audit.ResourceRef{Cluster: snap.Cluster, Namespace: d.Namespace, Kind: "Deployment", Name: d.Name},
			Title:     fmt.Sprintf("Deployment %q runs privileged containers", d.Name),
			Impact:    "A privileged container can access host devices and escape to the node — full node/cluster compromise if breached.",
			RootCause: fmt.Sprintf("privileged containers: %v", bad),
			Remediation: audit.Remediation{
				Summary: "Drop securityContext.privileged; grant only the specific Linux capabilities actually required.",
				KubectlEquivalent: fmt.Sprintf(
					"kubectl -n %s edit deployment/%s  # remove containers[].securityContext.privileged: true",
					d.Namespace, d.Name),
			},
			DetectedAt: timeNow(),
		})
	}
	return out
}

// HostPathVolume flags workloads mounting a host path, which breaks node isolation
// and is a common privilege-escalation and data-exfiltration vector.
type HostPathVolume struct{}

func (HostPathVolume) ID() string               { return "security.host_path_volume" }
func (HostPathVolume) Category() audit.Category { return audit.Security }

func (HostPathVolume) Evaluate(_ context.Context, snap *k8s.Snapshot) []audit.Finding {
	var out []audit.Finding
	for i := range snap.Deployments {
		d := &snap.Deployments[i]
		var paths []string
		for _, v := range d.Spec.Template.Spec.Volumes {
			if v.HostPath != nil {
				paths = append(paths, v.HostPath.Path)
			}
		}
		if len(paths) == 0 {
			continue
		}
		out = append(out, audit.Finding{
			RuleID:    "security.host_path_volume",
			Category:  audit.Security,
			Severity:  audit.P2,
			Resource:  audit.ResourceRef{Cluster: snap.Cluster, Namespace: d.Namespace, Kind: "Deployment", Name: d.Name},
			Title:     fmt.Sprintf("Deployment %q mounts host paths", d.Name),
			Impact:    "hostPath mounts break node isolation; a compromised pod can read/modify node files and escalate.",
			RootCause: fmt.Sprintf("hostPath volumes: %v", paths),
			Remediation: audit.Remediation{
				Summary: "Replace hostPath with a PVC, emptyDir, CSI volume, or projected volume scoped to the workload.",
			},
			DetectedAt: timeNow(),
		})
	}
	return out
}
