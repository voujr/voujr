// Package tools is the MCP-style tool layer: the only path by which the agent
// can affect the world. A tool is a name + JSON schema + handler. Every call
// flows through the registry's dispatch chain (validate → policy → approval →
// dry-run → execute → audit → redact), so safety is enforced in one place.
package tools

import (
	"context"
	"encoding/json"
)

// RiskLevel classifies a tool's blast radius. It drives the approval and
// read-only gates.
type RiskLevel int

const (
	// Read tools have no side effects; always allowed.
	Read RiskLevel = iota
	// Mutate tools change cluster/cloud state reversibly (patch, scale, sync).
	Mutate
	// Destructive tools are hard/impossible to reverse (delete, drain, tf apply).
	Destructive
)

func (r RiskLevel) String() string {
	switch r {
	case Read:
		return "read"
	case Mutate:
		return "mutate"
	case Destructive:
		return "destructive"
	default:
		return "unknown"
	}
}

// RawArgs is the unvalidated JSON the model produced for a tool call.
type RawArgs = json.RawMessage

// Result is the outcome of a tool execution.
type Result struct {
	// Summary is a short human-readable line for the TUI/audit log.
	Summary string
	// Data is structured output for programmatic use (rendered to tables, etc.).
	Data any
	// ModelView is what gets fed back into the prompt — usually trimmed and
	// always secret-redacted, so large/sensitive output never bloats or leaks
	// into the context window.
	ModelView string
	// Diff, when set, is the change preview shown at approval time.
	Diff string
	// RollbackRef points at a stored prior-state snapshot for one-command revert.
	RollbackRef string
}

// Tool is the contract every capability implements. It mirrors the MCP tool
// shape so tools can also be served over MCP to other clients.
type Tool interface {
	// Name is the stable identifier the model references, e.g. "kubectl_patch".
	Name() string
	// Description is model-facing; it should state what the tool does and when
	// to use it.
	Description() string
	// Schema is a JSON Schema object for the arguments. The registry validates
	// model output against it before Execute is reached.
	Schema() map[string]any
	// Risk classifies blast radius.
	Risk() RiskLevel
	// Execute runs the capability. By the time it is called, args are validated,
	// policy/RBAC have passed, and (for mutations) approval was granted. When
	// dryRun is true the tool must not persist changes (server-side dry-run).
	Execute(ctx context.Context, args RawArgs, dryRun bool) (Result, error)
}

// Mutating reports whether a tool needs the approval gate.
func Mutating(t Tool) bool { return t.Risk() >= Mutate }

// Spec converts a tool to its model-facing declaration.
func Spec(t Tool) Declaration {
	return Declaration{Name: t.Name(), Description: t.Description(), Schema: t.Schema()}
}

// Declaration is the model-facing tool spec (mirrors ai.ToolSpec to avoid an
// import cycle; the cmd layer adapts between them).
type Declaration struct {
	Name        string
	Description string
	Schema      map[string]any
}
