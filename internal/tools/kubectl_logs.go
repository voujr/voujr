package tools

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// KubectlLogs fetches recent logs from a pod container — the first thing to read
// when a container is crashing or erroring.
type KubectlLogs struct{ Clusters clusterSource }

func (KubectlLogs) Name() string { return "kubectl_logs" }

func (KubectlLogs) Description() string {
	return "Fetch recent logs from a pod, optionally a specific container or the previous " +
		"(crashed) instance. Use this to see why a container is failing or CrashLoopBackOff."
}

func (KubectlLogs) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"namespace", "pod"},
		"properties": map[string]any{
			"namespace": map[string]any{"type": "string"},
			"pod":       map[string]any{"type": "string"},
			"container": map[string]any{"type": "string", "description": "container name; default is the first container"},
			"tail":      map[string]any{"type": "integer", "description": "lines from the end (default 100)"},
			"previous":  map[string]any{"type": "boolean", "description": "logs from the previous terminated instance (for crash loops)"},
		},
	}
}

func (KubectlLogs) Risk() RiskLevel { return Read }

func (k KubectlLogs) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	var in struct {
		Namespace string `json:"namespace"`
		Pod       string `json:"pod"`
		Container string `json:"container"`
		Tail      int64  `json:"tail"`
		Previous  bool   `json:"previous"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if in.Tail <= 0 {
		in.Tail = 100
	}
	c, err := k.Clusters.Active()
	if err != nil {
		return Result{}, err
	}

	opts := &corev1.PodLogOptions{Container: in.Container, TailLines: &in.Tail, Previous: in.Previous}
	raw, err := c.Typed().CoreV1().Pods(in.Namespace).GetLogs(in.Pod, opts).DoRaw(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("get logs %s/%s: %w", in.Namespace, in.Pod, err)
	}
	logs := string(raw)
	if logs == "" {
		logs = "(no log output)"
	}
	return Result{
		Summary:   fmt.Sprintf("logs %s/%s (tail %d%s)", in.Namespace, in.Pod, in.Tail, prevLabel(in.Previous)),
		Data:      logs,
		ModelView: logs,
	}, nil
}

func prevLabel(prev bool) string {
	if prev {
		return ", previous"
	}
	return ""
}
