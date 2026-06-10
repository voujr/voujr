package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPrometheusQueryRendersSeries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "" {
			http.Error(w, "missing query", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector",` +
			`"result":[{"metric":{"service":"api","code":"500"},"value":[1700000000,"42"]}]}}`))
	}))
	defer srv.Close()

	tool := PrometheusQuery{BaseURL: srv.URL, HTTP: srv.Client()}
	args, _ := json.Marshal(map[string]any{"query": "sum(rate(http_requests_total[5m]))"})

	res, err := tool.Execute(context.Background(), args, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.ModelView, `service="api"`) || !strings.Contains(res.ModelView, "= 42") {
		t.Fatalf("unexpected render:\n%s", res.ModelView)
	}
}

func TestPrometheusQueryUnconfigured(t *testing.T) {
	_, err := PrometheusQuery{}.Execute(context.Background(), json.RawMessage(`{"query":"up"}`), false)
	if err == nil {
		t.Fatal("expected an error when prometheus_url is not configured")
	}
}

func TestPrometheusQueryServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"error","error":"bad query"}`))
	}))
	defer srv.Close()

	tool := PrometheusQuery{BaseURL: srv.URL, HTTP: srv.Client()}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"bogus("}`), false)
	if err == nil || !strings.Contains(err.Error(), "bad query") {
		t.Fatalf("expected prometheus error surfaced, got %v", err)
	}
}
