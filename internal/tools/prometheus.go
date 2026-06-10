package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// PrometheusQuery runs a PromQL instant query against the operator-configured
// Prometheus. The base URL is fixed at construction (never model-chosen) so a
// prompt-injected query can't be turned into an SSRF against arbitrary hosts.
type PrometheusQuery struct {
	BaseURL string
	HTTP    *http.Client
}

func (PrometheusQuery) Name() string { return "prometheus_query" }

func (PrometheusQuery) Description() string {
	return "Run a PromQL instant query against the configured Prometheus and return the " +
		"matching series. Use for metric-backed analysis: CPU/memory usage, error rates, " +
		"request latency, and saturation."
}

func (PrometheusQuery) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"query"},
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": `PromQL expression, e.g. sum(rate(http_requests_total{code=~"5.."}[5m])) by (service)`,
			},
		},
	}
}

func (PrometheusQuery) Risk() RiskLevel { return Read }

func (p PrometheusQuery) Execute(ctx context.Context, args RawArgs, _ bool) (Result, error) {
	if p.BaseURL == "" {
		return Result{}, fmt.Errorf("prometheus_url not configured")
	}
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Query) == "" {
		return Result{}, fmt.Errorf("query is required")
	}

	httpc := p.HTTP
	if httpc == nil {
		httpc = http.DefaultClient
	}
	endpoint := strings.TrimRight(p.BaseURL, "/") + "/api/v1/query?query=" + url.QueryEscape(in.Query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("prometheus: %w", err)
	}
	defer resp.Body.Close()

	var out struct {
		Status string `json:"status"`
		Error  string `json:"error"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []any             `json:"value"` // [ <unix_ts>, "<value>" ]
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Result{}, fmt.Errorf("decode prometheus response: %w", err)
	}
	if out.Status != "success" {
		return Result{}, fmt.Errorf("prometheus error: %s", out.Error)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "query: %s\n%d series:\n", in.Query, len(out.Data.Result))
	for i, r := range out.Data.Result {
		if i >= 50 {
			fmt.Fprintf(&b, "  …(%d more)\n", len(out.Data.Result)-50)
			break
		}
		val := ""
		if len(r.Value) == 2 {
			val = fmt.Sprintf("%v", r.Value[1])
		}
		fmt.Fprintf(&b, "  %s = %s\n", labelsString(r.Metric), val)
	}

	return Result{
		Summary:   fmt.Sprintf("prometheus: %d series", len(out.Data.Result)),
		Data:      out.Data.Result,
		ModelView: b.String(),
	}, nil
}

func labelsString(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+`="`+m[k]+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
