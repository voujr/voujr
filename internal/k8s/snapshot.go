package k8s

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Snapshot is a coherent point-in-time view passed to audit rules so a scan sees
// a consistent picture. In controller mode it is served from the informer cache;
// in CLI mode it is a bounded live list.
type Snapshot struct {
	Cluster     string
	Pods        []corev1.Pod
	Deployments []appsv1.Deployment
	Nodes       []corev1.Node
}

// Snapshot collects the resources the audit engine reasons over. namespace ""
// means all namespaces.
func (c *Cluster) Snapshot(ctx context.Context, namespace string) (*Snapshot, error) {
	pods, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	deps, err := c.typed.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	nodes, err := c.typed.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	return &Snapshot{
		Cluster:     c.Name,
		Pods:        pods.Items,
		Deployments: deps.Items,
		Nodes:       nodes.Items,
	}, nil
}

// ContextCard renders a compact, token-budgeted summary of cluster state for the
// model prompt — grounding without dumping raw YAML.
func (s *Snapshot) ContextCard() string {
	var crashloop, pending int
	for _, p := range s.Pods {
		switch p.Status.Phase {
		case corev1.PodPending:
			pending++
		}
		for _, cs := range p.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				crashloop++
			}
		}
	}
	var notProgressing int
	for _, d := range s.Deployments {
		if d.Status.ReadyReplicas < d.Status.Replicas {
			notProgressing++
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "cluster: %s (%d nodes)\n", s.Cluster, len(s.Nodes))
	fmt.Fprintf(&b, "workloads: %d deployments, %d pods\n", len(s.Deployments), len(s.Pods))
	fmt.Fprintf(&b, "health: %d CrashLoopBackOff, %d pending, %d deployments degraded\n",
		crashloop, pending, notProgressing)
	return b.String()
}
