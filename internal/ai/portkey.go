package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Portkey is the default Provider: a unified gateway in front of OpenAI,
// Anthropic, and Gemini. Routing/failover/BYOK are expressed as a per-request
// "config" sent in the x-portkey-config header, so we keep a single transport.
//
// See https://portkey.ai — the chat API is OpenAI-compatible at /v1/chat/completions.
type Portkey struct {
	baseURL string
	apiKey  string
	http    *http.Client
	models  []ModelInfo
}

// NewPortkey constructs a gateway-backed provider. baseURL defaults to the
// hosted gateway when empty.
func NewPortkey(baseURL, apiKey string, timeout time.Duration, models []ModelInfo) *Portkey {
	if baseURL == "" {
		baseURL = "https://api.portkey.ai/v1"
	}
	return &Portkey{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http:    &http.Client{Timeout: timeout},
		models:  models,
	}
}

func (p *Portkey) Name() string        { return "portkey" }
func (p *Portkey) Models() []ModelInfo { return p.models }

// portkeyConfig is the gateway routing directive. A "fallback" strategy makes
// the gateway transparently retry the next target on transport failure.
type portkeyConfig struct {
	Strategy struct {
		Mode string `json:"mode"` // "fallback" | "loadbalance"
	} `json:"strategy"`
	Targets []portkeyTarget `json:"targets"`
}

type portkeyTarget struct {
	// VirtualKey references a BYOK key stored in Portkey, so raw provider keys
	// never live in our process/config.
	VirtualKey string `json:"virtual_key,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
}

func (p *Portkey) buildConfig(r Routing) portkeyConfig {
	var cfg portkeyConfig
	cfg.Strategy.Mode = "fallback"
	add := func(ref string) {
		provider, model := splitRef(ref)
		cfg.Targets = append(cfg.Targets, portkeyTarget{Provider: provider, Model: model})
	}
	add(r.Model)
	for _, f := range r.Fallbacks {
		add(f)
	}
	return cfg
}

// splitRef parses "provider/model" into its parts.
func splitRef(ref string) (provider, model string) {
	if i := strings.IndexByte(ref, '/'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "", ref
}

// --- OpenAI-compatible wire types (subset) ---

type oaiMessage struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type oaiRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Tools       []oaiTool    `json:"tools,omitempty"`
	Temperature float32      `json:"temperature,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Stream      bool         `json:"stream,omitempty"`
}

func toWireMessages(in []Message) []oaiMessage {
	out := make([]oaiMessage, 0, len(in))
	for _, m := range in {
		wm := oaiMessage{Role: string(m.Role), Content: m.Content, ToolCallID: m.ToolCallID, Name: m.Name}
		for _, tc := range m.ToolCalls {
			var c oaiToolCall
			c.ID, c.Type = tc.ID, "function"
			c.Function.Name = tc.Name
			c.Function.Arguments = string(tc.Args)
			wm.ToolCalls = append(wm.ToolCalls, c)
		}
		out = append(out, wm)
	}
	return out
}

func toWireTools(in []ToolSpec) []oaiTool {
	out := make([]oaiTool, 0, len(in))
	for _, t := range in {
		var wt oaiTool
		wt.Type = "function"
		wt.Function.Name = t.Name
		wt.Function.Description = t.Description
		wt.Function.Parameters = t.Schema
		out = append(out, wt)
	}
	return out
}

func (p *Portkey) newRequest(ctx context.Context, req Request, stream bool) (*http.Request, error) {
	model := req.Routing.Model
	_, bare := splitRef(model)
	body := oaiRequest{
		Model:       bare,
		Messages:    toWireMessages(req.Messages),
		Tools:       toWireTools(req.Tools),
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      stream,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	cfg, _ := json.Marshal(p.buildConfig(req.Routing))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("x-portkey-config", string(cfg))
	for k, v := range req.Metadata {
		httpReq.Header.Set("x-portkey-metadata-"+k, v)
	}
	return httpReq, nil
}

// Chat performs a blocking completion.
func (p *Portkey) Chat(ctx context.Context, req Request) (Response, error) {
	httpReq, err := p.newRequest(ctx, req, false)
	if err != nil {
		return Response{}, err
	}
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("portkey: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return Response{}, fmt.Errorf("portkey status %d: %s", resp.StatusCode, b)
	}

	var out struct {
		Choices []struct {
			Message oaiMessage `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Model string `json:"model"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Response{}, err
	}
	if len(out.Choices) == 0 {
		return Response{}, fmt.Errorf("portkey: empty response")
	}
	msg := fromWireMessage(out.Choices[0].Message)
	return Response{
		Message: msg,
		Usage: Usage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
			Model:        out.Model,
			CostCents:    p.estimateCost(out.Model, out.Usage.PromptTokens, out.Usage.CompletionTokens),
		},
	}, nil
}

func fromWireMessage(m oaiMessage) Message {
	out := Message{Role: Role(m.Role), Content: m.Content}
	for _, tc := range m.ToolCalls {
		out.ToolCalls = append(out.ToolCalls, ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: []byte(tc.Function.Arguments),
		})
	}
	return out
}

func (p *Portkey) estimateCost(model string, in, out int) float64 {
	for _, m := range p.models {
		if strings.HasSuffix(m.Ref, model) || m.Ref == model {
			return float64(in)/1e6*m.InputCentsPerMTok + float64(out)/1e6*m.OutputCentsPerMTok
		}
	}
	return 0
}

// Stream performs a streaming completion over SSE.
func (p *Portkey) Stream(ctx context.Context, req Request) (Stream, error) {
	httpReq, err := p.newRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "text/event-stream")
	resp, err := p.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("portkey: %w", err)
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("portkey status %d: %s", resp.StatusCode, b)
	}
	return &sseStream{r: bufio.NewReader(resp.Body), c: resp.Body}, nil
}

// Embed is delegated to the gateway's embeddings endpoint (omitted here for
// brevity; the shape mirrors Chat). Returned for interface completeness.
func (p *Portkey) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	return nil, fmt.Errorf("portkey: Embed not implemented in scaffold")
}

// sseStream parses an OpenAI-compatible Server-Sent Events stream, reassembling
// streamed tool-call argument fragments into complete ToolCalls.
type sseStream struct {
	r       *bufio.Reader
	c       io.Closer
	pending map[int]*ToolCall // index -> accumulating call
	flush   []ToolCall        // completed calls awaiting emission, in index order
}

func (s *sseStream) Recv() (Delta, error) {
	if s.pending == nil {
		s.pending = map[int]*ToolCall{}
	}
	// Emit any completed tool calls one per Recv before reading more.
	if len(s.flush) > 0 {
		tc := s.flush[0]
		s.flush = s.flush[1:]
		return Delta{ToolCall: &tc}, nil
	}
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return Delta{Done: true}, err
		}
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			return Delta{Done: true}, nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			return Delta{Text: ch.Delta.Content}, nil
		}
		for _, tc := range ch.Delta.ToolCalls {
			cur := s.pending[tc.Index]
			if cur == nil {
				cur = &ToolCall{}
				s.pending[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Function.Name != "" {
				cur.Name = tc.Function.Name
			}
			cur.Args = append(cur.Args, []byte(tc.Function.Arguments)...)
		}
		if ch.FinishReason != nil && *ch.FinishReason == "tool_calls" {
			// All fragments are in; queue every pending call in index order and
			// emit the first now. The rest drain on subsequent Recv calls.
			s.queuePending()
			if len(s.flush) > 0 {
				tc := s.flush[0]
				s.flush = s.flush[1:]
				return Delta{ToolCall: &tc}, nil
			}
		}
	}
}

// queuePending moves accumulated tool calls into the flush queue, ordered by the
// stream's tool-call index so arguments map to the right call.
func (s *sseStream) queuePending() {
	idxs := make([]int, 0, len(s.pending))
	for i := range s.pending {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		s.flush = append(s.flush, *s.pending[i])
		delete(s.pending, i)
	}
}

func (s *sseStream) Close() error { return s.c.Close() }
