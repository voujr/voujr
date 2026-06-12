package incident

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/voujr/voujr/internal/audit"
)

// pdEventsURL is the PagerDuty Events API v2 enqueue endpoint.
const pdEventsURL = "https://events.pagerduty.com/v2/enqueue"

// triggerPagerDuty raises a PagerDuty incident for a finding via Events API v2.
// The finding's dedup key is reused so a recurring issue updates one incident
// instead of spawning many.
func (n *Notifier) triggerPagerDuty(ctx context.Context, f audit.Finding) error {
	payload := map[string]any{
		"routing_key":  n.pagerDutyKey,
		"event_action": "trigger",
		"dedup_key":    f.DedupKey(),
		"payload": map[string]any{
			"summary":   fmt.Sprintf("[%s] %s — %s/%s", f.Severity, f.Title, f.Resource.Namespace, f.Resource.Name),
			"severity":  pdSeverity(f.Severity),
			"source":    f.Resource.Cluster,
			"component": f.Resource.Kind + "/" + f.Resource.Name,
			"group":     string(f.Category),
			"class":     f.RuleID,
			"custom_details": map[string]string{
				"impact":      f.Impact,
				"root_cause":  f.RootCause,
				"remediation": f.Remediation.Summary,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.pagerDutyURL(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("pagerduty status %d", resp.StatusCode)
	}
	return nil
}

// pagerDutyURL allows tests to override the endpoint; defaults to the real API.
func (n *Notifier) pagerDutyURL() string {
	if n.pdURLOverride != "" {
		return n.pdURLOverride
	}
	return pdEventsURL
}

// pdSeverity maps voujr severities to PagerDuty's severity vocabulary.
func pdSeverity(s audit.Severity) string {
	switch s {
	case audit.P0:
		return "critical"
	case audit.P1:
		return "error"
	case audit.P2:
		return "warning"
	default:
		return "info"
	}
}
