package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// KubectlEvents lists recent events — the fastest way to spot scheduling
// failures, probe failures, image pull errors, OOMKills, and evictions.
type KubectlEvents struct{ Clusters clusterSource }

func (KubectlEvents) Name() string { return "kubectl_events" }

func (KubectlEvents) Description() string {
	return "List recent events in a namespace (optionally warnings only), newest first. " +
		"Surfaces FailedScheduling, Unhealthy probes, ImagePullBackOff, OOMKilled, and evictions."
}

func (KubectlEvents) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace":     map[string]any{"type": "string", "description": "namespace; empty scans all namespaces"},
			"warnings_only": map[string]any{"type": "boolean"},
			"limit":         map[string]any{"type": "integer", "description": "max events (default 30)"},
		},
	}
}

func (KubectlEvents) Risk() RiskLevel { return Read }

func (k KubectlEvents) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	var in struct {
		Namespace    string `json:"namespace"`
		WarningsOnly bool   `json:"warnings_only"`
		Limit        int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &in)
	if in.Limit <= 0 {
		in.Limit = 30
	}
	c, err := k.Clusters.Active()
	if err != nil {
		return Result{}, err
	}
	list, err := c.Typed().CoreV1().Events(in.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Result{}, err
	}
	items := list.Items
	if in.WarningsOnly {
		filtered := items[:0:0]
		for _, e := range items {
			if e.Type == corev1.EventTypeWarning {
				filtered = append(filtered, e)
			}
		}
		items = filtered
	}

	var b strings.Builder
	kind := "events"
	if in.WarningsOnly {
		kind = "warning events"
	}
	fmt.Fprintf(&b, "%d %s in %s:\n", len(items), kind, nsLabel(in.Namespace))
	writeEvents(&b, items, in.Limit)

	return Result{
		Summary:   fmt.Sprintf("%d %s in %s", len(items), kind, nsLabel(in.Namespace)),
		Data:      items,
		ModelView: b.String(),
	}, nil
}

// writeEvents renders events newest-first, capped at limit. Shared with describe.
func writeEvents(b *strings.Builder, items []corev1.Event, limit int) {
	sort.SliceStable(items, func(i, j int) bool { return eventTime(items[i]).After(eventTime(items[j])) })
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	for _, e := range items {
		obj := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
		fmt.Fprintf(b, "  %-7s %-22s %-28s %s\n", e.Type, e.Reason, obj, oneLineMsg(e.Message))
	}
}

func eventTime(e corev1.Event) time.Time {
	if !e.LastTimestamp.IsZero() {
		return e.LastTimestamp.Time
	}
	if !e.EventTime.IsZero() {
		return e.EventTime.Time
	}
	return e.CreationTimestamp.Time
}

func oneLineMsg(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 100 {
		return s[:100] + "…"
	}
	return s
}
