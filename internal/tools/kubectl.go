package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/voujr/voujr/internal/k8s"
)

// clusterSource lets tools resolve the session's active cluster without coupling
// to the full registry type.
type clusterSource interface {
	Active() (*k8s.Cluster, error)
}

// --- kubectl_get (Read) ---------------------------------------------------

// KubectlGet lists pods in a namespace with their phase and restart counts. It
// is the workhorse read tool; richer get/describe/logs variants follow the same
// shape.
type KubectlGet struct{ Clusters clusterSource }

func (KubectlGet) Name() string { return "kubectl_get_pods" }

func (KubectlGet) Description() string {
	return "List pods in a namespace with phase, ready state, and restart counts. " +
		"Use this to observe workload health before deeper investigation."
}

func (KubectlGet) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace": map[string]any{
				"type":        "string",
				"description": "Namespace to list; empty lists all namespaces.",
			},
		},
	}
}

func (KubectlGet) Risk() RiskLevel { return Read }

func (k KubectlGet) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	var in struct {
		Namespace string `json:"namespace"`
	}
	_ = json.Unmarshal(args, &in)

	c, err := k.Clusters.Active()
	if err != nil {
		return Result{}, err
	}
	pods, err := c.ListPods(ctx, in.Namespace)
	if err != nil {
		return Result{}, err
	}

	type row struct {
		Namespace, Name, Phase string
		Restarts               int32
	}
	rows := make([]row, 0, len(pods))
	var model strings.Builder
	fmt.Fprintf(&model, "%d pods in %q:\n", len(pods), nsLabel(in.Namespace))
	for _, p := range pods {
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
		rows = append(rows, row{p.Namespace, p.Name, string(p.Status.Phase), restarts})
		fmt.Fprintf(&model, "  %s/%s  %s  restarts=%d\n", p.Namespace, p.Name, p.Status.Phase, restarts)
	}
	return Result{
		Summary:   fmt.Sprintf("listed %d pods in %s", len(pods), nsLabel(in.Namespace)),
		Data:      rows,
		ModelView: model.String(),
	}, nil
}

// --- kubectl_scale (Mutate) ----------------------------------------------

// KubectlScale changes a Deployment's replica count. It demonstrates the
// mutating path: RBAC preflight, server-side dry-run for the diff, then apply.
type KubectlScale struct{ Clusters clusterSource }

func (KubectlScale) Name() string { return "kubectl_scale_deployment" }

func (KubectlScale) Description() string {
	return "Scale a Deployment to a target replica count. Mutating: requires approval."
}

func (KubectlScale) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"namespace", "name", "replicas"},
		"properties": map[string]any{
			"namespace": map[string]any{"type": "string"},
			"name":      map[string]any{"type": "string"},
			"replicas":  map[string]any{"type": "integer", "description": "target replica count"},
		},
	}
}

func (KubectlScale) Risk() RiskLevel { return Mutate }

func (k KubectlScale) Execute(ctx context.Context, args RawArgs, dryRun bool) (Result, error) {
	var in struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Replicas  int32  `json:"replicas"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	c, err := k.Clusters.Active()
	if err != nil {
		return Result{}, err
	}

	// RBAC preflight — fail loud and specific if the caller can't do this.
	allowed, err := c.CanI(ctx, "patch", "apps", "deployments", in.Namespace)
	if err != nil {
		return Result{}, fmt.Errorf("rbac check: %w", err)
	}
	if !allowed {
		return Result{}, fmt.Errorf("denied: missing RBAC apps/deployments:patch in %s", in.Namespace)
	}

	cur, err := c.Typed().AppsV1().Deployments(in.Namespace).Get(ctx, in.Name, metav1.GetOptions{})
	if err != nil {
		return Result{}, err
	}
	from := int32(1)
	if cur.Spec.Replicas != nil {
		from = *cur.Spec.Replicas
	}
	diff := fmt.Sprintf("deployment/%s replicas: %d → %d", in.Name, from, in.Replicas)

	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, in.Replicas))
	opts := metav1.PatchOptions{}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if _, err := c.Typed().AppsV1().Deployments(in.Namespace).
		Patch(ctx, in.Name, types.MergePatchType, patch, opts); err != nil {
		return Result{}, err
	}

	// Snapshot prior state so the change can be rolled back with one command.
	rollback := snapshotRef(cur)

	res := Result{
		Summary:     diff,
		Diff:        diff,
		RollbackRef: rollback,
		ModelView:   diff,
	}
	if dryRun {
		res.Summary = "[dry-run] " + diff
	}
	return res, nil
}

func nsLabel(ns string) string {
	if ns == "" {
		return "all namespaces"
	}
	return ns
}

// snapshotRef serializes the prior object for rollback storage. A real impl
// persists this to the store keyed by a generated id; here we return the JSON.
func snapshotRef(d *appsv1.Deployment) string {
	b, _ := json.Marshal(map[string]any{
		"kind": "Deployment", "namespace": d.Namespace, "name": d.Name,
		"replicas": d.Spec.Replicas,
	})
	return string(b)
}
