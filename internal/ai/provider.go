// Package ai is the provider-agnostic orchestration layer. The agent runtime
// depends only on the Provider interface and these neutral message types; the
// concrete transport (Portkey gateway or a direct SDK) is an implementation
// detail selected at wiring time.
package ai

import "context"

// Role identifies the author of a message in the conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool" // a tool result fed back into the loop
)

// Message is one item in the conversation, normalized across providers.
type Message struct {
	Role    Role
	Content string

	// ToolCalls are emitted by the assistant when it wants to act. A single
	// assistant turn may request several tools.
	ToolCalls []ToolCall

	// ToolCallID links a RoleTool message back to the ToolCall it answers.
	ToolCallID string

	// Name is the tool name for RoleTool messages.
	Name string
}

// ToolCall is the model's request to invoke a tool with JSON arguments.
type ToolCall struct {
	ID   string
	Name string
	// Args is raw JSON; the registry validates it against the tool's schema
	// before anything executes.
	Args []byte
}

// ToolSpec is the model-facing declaration of an available tool.
type ToolSpec struct {
	Name        string
	Description string
	// Schema is a JSON Schema object describing the arguments.
	Schema map[string]any
}

// Routing carries hints from the model router to the provider/gateway.
type Routing struct {
	// Model is the resolved primary, e.g. "anthropic/claude-opus-4-8".
	Model string
	// Fallbacks are tried (gateway-side) on transport failure, in order.
	Fallbacks []string
	// Reason is recorded for observability.
	Reason string
}

// Request is a single inference request.
type Request struct {
	Messages    []Message
	Tools       []ToolSpec
	Temperature float32
	MaxTokens   int
	Routing     Routing
	// Metadata is forwarded to the gateway for conditional routing / tracing.
	Metadata map[string]string
}

// Usage reports token consumption and estimated cost for accounting.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CostCents    float64
	Model        string
}

// Response is a complete (non-streamed) inference result.
type Response struct {
	Message Message
	Usage   Usage
}

// Delta is one streamed increment: either a text fragment or a partial tool call.
type Delta struct {
	Text     string
	ToolCall *ToolCall // populated once a tool call is fully assembled
	Done     bool
	Usage    *Usage // present on the final delta
	Err      error
}

// Stream yields Deltas until Done. Callers must drain or cancel via context.
type Stream interface {
	Recv() (Delta, error)
	Close() error
}

// ModelInfo describes a model's capabilities for the router.
type ModelInfo struct {
	Ref                string // "provider/model"
	ContextWindow      int
	InputCentsPerMTok  float64
	OutputCentsPerMTok float64
	// Tier classifies the model: "fast" | "reasoning" | "long".
	Tier string
}

// Provider is the single abstraction the runtime depends on.
type Provider interface {
	// Chat performs a blocking inference.
	Chat(ctx context.Context, req Request) (Response, error)
	// Stream performs a streaming inference.
	Stream(ctx context.Context, req Request) (Stream, error)
	// Embed returns embeddings for long-term memory recall.
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	// Name identifies the provider/gateway.
	Name() string
	// Models lists known models for routing.
	Models() []ModelInfo
}
