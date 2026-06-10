package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubectlDescribe renders the status, conditions, container restart reasons, and
// recent events for a single pod or deployment — the primary diagnosis tool.
type KubectlDescribe struct{ Clusters clusterSource }

func (KubectlDescribe) Name() string { return "kubectl_describe" }

func (KubectlDescribe) Description() string {
	return "Describe a pod or deployment: status, conditions, container restart reasons and " +
		"last-termination state, plus recent events for that object. The go-to tool for " +
		"diagnosing why a workload is unhealthy."
}

func (KubectlDescribe) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"namespace", "kind", "name"},
		"properties": map[string]any{
			"namespace": map[string]any{"type": "string"},
			"kind":      map[string]any{"type": "string", "enum": []any{"pod", "deployment"}},
			"name":      map[string]any{"type": "string"},
		},
	}
}

func (KubectlDescribe) Risk() RiskLevel { return Read }

func (k KubectlDescribe) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	var in struct {
		Namespace string `json:"namespace"`
		Kind      string `json:"kind"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	c, err := k.Clusters.Active()
	if err != nil {
		return Result{}, err
	}

	var b strings.Builder
	switch strings.ToLower(in.Kind) {
	case "pod":
		p, err := c.Typed().CoreV1().Pods(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		describePod(&b, p)
	case "deployment":
		d, err := c.Typed().AppsV1().Deployments(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return Result{}, err
		}
		describeDeployment(&b, d)
	default:
		return Result{}, fmt.Errorf("unsupported kind %q (use pod or deployment)", in.Kind)
	}

	if evs, err := c.Typed().CoreV1().Events(in.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + in.Name,
	}); err == nil && len(evs.Items) > 0 {
		b.WriteString("Events:\n")
		writeEvents(&b, evs.Items, 8)
	}

	out := b.String()
	return Result{
		Summary:   fmt.Sprintf("described %s/%s", in.Kind, in.Name),
		Data:      out,
		ModelView: out,
	}, nil
}

func describePod(b *strings.Builder, p *corev1.Pod) {
	fmt.Fprintf(b, "Pod %s/%s  phase=%s  node=%s\n", p.Namespace, p.Name, p.Status.Phase, p.Spec.NodeName)
	for _, cond := range p.Status.Conditions {
		fmt.Fprintf(b, "  condition %s=%s", cond.Type, cond.Status)
		if cond.Reason != "" {
			fmt.Fprintf(b, " (%s: %s)", cond.Reason, oneLineMsg(cond.Message))
		}
		b.WriteString("\n")
	}
	for _, cs := range p.Status.ContainerStatuses {
		fmt.Fprintf(b, "  container %s ready=%t restarts=%d\n", cs.Name, cs.Ready, cs.RestartCount)
		if w := cs.State.Waiting; w != nil {
			fmt.Fprintf(b, "    waiting: %s — %s\n", w.Reason, oneLineMsg(w.Message))
		}
		if t := cs.State.Terminated; t != nil {
			fmt.Fprintf(b, "    terminated: %s exit=%d\n", t.Reason, t.ExitCode)
		}
		if t := cs.LastTerminationState.Terminated; t != nil {
			fmt.Fprintf(b, "    last-terminated: %s exit=%d\n", t.Reason, t.ExitCode)
		}
	}
}

func describeDeployment(b *strings.Builder, d *appsv1.Deployment) {
	fmt.Fprintf(b, "Deployment %s/%s  replicas=%d ready=%d available=%d updated=%d\n",
		d.Namespace, d.Name, d.Status.Replicas, d.Status.ReadyReplicas,
		d.Status.AvailableReplicas, d.Status.UpdatedReplicas)
	for _, cond := range d.Status.Conditions {
		fmt.Fprintf(b, "  condition %s=%s", cond.Type, cond.Status)
		if cond.Reason != "" {
			fmt.Fprintf(b, " (%s: %s)", cond.Reason, oneLineMsg(cond.Message))
		}
		b.WriteString("\n")
	}
}
