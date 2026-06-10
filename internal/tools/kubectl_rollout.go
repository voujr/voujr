package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// KubectlRolloutRestart triggers a rolling restart of a Deployment by stamping
// the standard restart annotation on its pod template — the same mechanism as
// `kubectl rollout restart`. Mutating: it flows through the approval chain.
type KubectlRolloutRestart struct{ Clusters clusterSource }

func (KubectlRolloutRestart) Name() string { return "kubectl_rollout_restart" }

func (KubectlRolloutRestart) Description() string {
	return "Roll a Deployment's pods by bumping its restart annotation (recycle pods to clear a " +
		"transient bad state or re-read mounted config/secrets). Mutating: requires approval."
}

func (KubectlRolloutRestart) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"namespace", "name"},
		"properties": map[string]any{
			"namespace": map[string]any{"type": "string"},
			"name":      map[string]any{"type": "string"},
		},
	}
}

func (KubectlRolloutRestart) Risk() RiskLevel { return Mutate }

func (k KubectlRolloutRestart) Execute(ctx context.Context, args RawArgs, dryRun bool) (Result, error) {
	var in struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	c, err := k.Clusters.Active()
	if err != nil {
		return Result{}, err
	}

	// RBAC preflight — report the exact missing verb rather than failing mid-apply.
	allowed, err := c.CanI(ctx, "patch", "apps", "deployments", in.Namespace)
	if err != nil {
		return Result{}, fmt.Errorf("rbac check: %w", err)
	}
	if !allowed {
		return Result{}, fmt.Errorf("denied: missing RBAC apps/deployments:patch in %s", in.Namespace)
	}

	ts := time.Now().UTC().Format(time.RFC3339)
	patch := []byte(fmt.Sprintf(
		`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, ts))

	opts := metav1.PatchOptions{}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if _, err := c.Typed().AppsV1().Deployments(in.Namespace).
		Patch(ctx, in.Name, types.StrategicMergePatchType, patch, opts); err != nil {
		return Result{}, err
	}

	diff := fmt.Sprintf("rollout restart deployment/%s (restartedAt=%s)", in.Name, ts)
	res := Result{Summary: diff, Diff: diff, ModelView: diff}
	if dryRun {
		res.Summary = "[dry-run] " + diff
	}
	return res, nil
}
