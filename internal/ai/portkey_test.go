package ai

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

// TestSSEStreamAssemblesTextToolCallsAndUsage feeds a canned OpenAI-compatible
// SSE body (with split tool-call arguments and a final include_usage chunk) and
// verifies the parser reassembles text, the tool call, and token usage.
func TestSSEStreamAssemblesTextToolCallsAndUsage(t *testing.T) {
	body := strings.Join([]string{
		`data: {"model":"claude-opus-4-8","choices":[{"delta":{"content":"Hello"}}]}`,
		`data: {"model":"claude-opus-4-8","choices":[{"delta":{"content":" world"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"kubectl_get_pods","arguments":"{\"namespace\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"prod\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: {"model":"claude-opus-4-8","choices":[],"usage":{"prompt_tokens":120,"completion_tokens":30}}`,
		`data: [DONE]`,
		"",
	}, "\n")

	s := &sseStream{r: bufio.NewReader(strings.NewReader(body)), c: io.NopCloser(strings.NewReader(""))}

	var text strings.Builder
	var calls []ToolCall
	var usage *Usage
	for {
		d, err := s.Recv()
		if d.Text != "" {
			text.WriteString(d.Text)
		}
		if d.ToolCall != nil {
			calls = append(calls, *d.ToolCall)
		}
		if d.Usage != nil {
			usage = d.Usage
		}
		if d.Done {
			break
		}
		if err != nil {
			t.Fatalf("unexpected pre-Done error: %v", err)
		}
	}

	if text.String() != "Hello world" {
		t.Fatalf("text = %q, want %q", text.String(), "Hello world")
	}
	if len(calls) != 1 {
		t.Fatalf("want 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "kubectl_get_pods" {
		t.Fatalf("tool name = %q", calls[0].Name)
	}
	if string(calls[0].Args) != `{"namespace":"prod"}` {
		t.Fatalf("split args not reassembled: %q", calls[0].Args)
	}
	if usage == nil {
		t.Fatal("usage not captured from include_usage chunk")
	}
	if usage.InputTokens != 120 || usage.OutputTokens != 30 || usage.Model != "claude-opus-4-8" {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestEstimateCostResolvesBareModelBySuffix(t *testing.T) {
	models := []ModelInfo{
		{Ref: "anthropic/claude-opus-4-8", InputCentsPerMTok: 1500, OutputCentsPerMTok: 7500},
	}
	// 1M input + 1M output → 1500 + 7500 = 9000 cents, even though the streamed
	// usage model omits the "anthropic/" provider prefix.
	got := EstimateCost(models, "claude-opus-4-8", 1_000_000, 1_000_000)
	if got != 9000 {
		t.Fatalf("EstimateCost = %v, want 9000", got)
	}
	if EstimateCost(models, "unknown-model", 1000, 1000) != 0 {
		t.Fatal("unknown model should cost 0")
	}
}
