// Package incident routes high-severity audit findings to alerting sinks (Slack,
// PagerDuty) with enrichment: severity, impact, root cause, and the proposed
// remediation. P0/P1 escalate; lower severities are summarized.
package incident

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/voujr/voujr/internal/audit"
)

// Notifier sends enriched alerts for findings at or above a threshold to any
// configured sink (Slack and/or PagerDuty).
type Notifier struct {
	slackWebhook string
	pagerDutyKey string
	threshold    audit.Severity
	http         *http.Client

	// pdURLOverride redirects the PagerDuty endpoint in tests; empty = real API.
	pdURLOverride string
}

// NewNotifier builds a notifier. Either sink may be empty (disabled). threshold
// is the minimum severity to escalate (e.g. P1 escalates P0+P1).
func NewNotifier(slackWebhook, pagerDutyKey string, threshold audit.Severity, c *http.Client) *Notifier {
	if c == nil {
		c = http.DefaultClient
	}
	if threshold == "" {
		threshold = audit.P1
	}
	return &Notifier{
		slackWebhook: slackWebhook,
		pagerDutyKey: pagerDutyKey,
		threshold:    threshold,
		http:         c,
	}
}

// Enabled reports whether any sink is configured.
func (n *Notifier) Enabled() bool { return n.slackWebhook != "" || n.pagerDutyKey != "" }

// Notify sends alerts for the qualifying findings and returns how many fired.
func (n *Notifier) Notify(ctx context.Context, findings []audit.Finding) (int, error) {
	if !n.Enabled() {
		return 0, nil
	}
	var fired int
	for _, f := range findings {
		if !atOrAbove(f.Severity, n.threshold) {
			continue
		}
		if n.slackWebhook != "" {
			if err := n.postSlack(ctx, f); err != nil {
				return fired, err
			}
		}
		if n.pagerDutyKey != "" {
			if err := n.triggerPagerDuty(ctx, f); err != nil {
				return fired, err
			}
		}
		fired++
	}
	return fired, nil
}

func (n *Notifier) postSlack(ctx context.Context, f audit.Finding) error {
	text := fmt.Sprintf(
		"*[%s] %s*\n*Cluster:* %s  *Resource:* %s/%s\n*Impact:* %s\n*Root cause:* %s\n*Fix:* %s",
		f.Severity, f.Title,
		f.Resource.Cluster, f.Resource.Namespace, f.Resource.Name,
		f.Impact, f.RootCause, f.Remediation.Summary,
	)
	body, _ := json.Marshal(map[string]string{"text": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.slackWebhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack webhook status %d", resp.StatusCode)
	}
	return nil
}

// atOrAbove reports whether sev is as or more severe than threshold (P0 highest).
func atOrAbove(sev, threshold audit.Severity) bool {
	rank := map[audit.Severity]int{audit.P0: 0, audit.P1: 1, audit.P2: 2, audit.P3: 3}
	return rank[sev] <= rank[threshold]
}
