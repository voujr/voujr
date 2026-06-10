// Package config loads and validates layered configuration for voujr.
//
// Precedence (highest first): flags > env > project file (./voujr.yaml) >
// user file (~/.voujr/config.yaml) > built-in defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode controls how much authority the agent has over a cluster.
type Mode string

const (
	// ModeReadOnly advertises and permits only read tools. Default.
	ModeReadOnly Mode = "read-only"
	// ModePropose lets the model plan mutations but every apply needs approval.
	ModePropose Mode = "propose"
	// ModeApply allows mutations; low-risk classes may be auto-approved per policy.
	ModeApply Mode = "apply"
)

// Config is the fully-resolved runtime configuration.
type Config struct {
	// Cluster selection.
	Context string `yaml:"context"`  // kube-context name; empty = current-context
	Mode    Mode   `yaml:"mode"`     // read-only | propose | apply
	DataDir string `yaml:"data_dir"` // where the local DB + logs live

	AI           AIConfig           `yaml:"ai"`
	Audit        AuditConfig        `yaml:"audit"`
	Tools        ToolsConfig        `yaml:"tools"`
	Observe      ObserveConfig      `yaml:"observability"`
	Incident     IncidentConfig     `yaml:"incident"`
	Integrations IntegrationsConfig `yaml:"integrations"`
}

// IntegrationsConfig holds endpoints for external observability/GitOps systems
// the tools talk to. URLs are operator-configured (never model-chosen) to avoid
// SSRF via prompt injection.
type IntegrationsConfig struct {
	PrometheusURL string `yaml:"prometheus_url"`
}

// AIConfig configures the orchestration layer.
type AIConfig struct {
	// Gateway is "portkey" (default) or "direct".
	Gateway string `yaml:"gateway"`
	// BaseURL overrides the gateway endpoint (self-hosted Portkey, proxies).
	BaseURL string `yaml:"base_url"`
	// DefaultProvider is used when routing is ambiguous: openai|anthropic|gemini.
	DefaultProvider string `yaml:"default_provider"`
	// Tiers maps a routing tier to a concrete model reference.
	Tiers map[string]string `yaml:"tiers"`
	// EmbeddingModel ("provider/model") powers long-term memory recall; empty
	// disables embeddings (recall degrades to nothing, gracefully).
	EmbeddingModel string `yaml:"embedding_model"`
	// BudgetCents caps spend per session; 0 = unlimited.
	BudgetCents int `yaml:"budget_cents"`
	// Timeout per model call.
	Timeout time.Duration `yaml:"timeout"`
	// keys are read from env, never persisted; see APIKey().
}

// AuditConfig controls the audit engine.
type AuditConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"` // 0 = on-demand only
	// DisabledRules lists rule IDs to skip (e.g. "security.privileged").
	DisabledRules []string `yaml:"disabled_rules"`
}

// ToolsConfig controls which tools are available and approval behavior.
type ToolsConfig struct {
	// Enabled is the allow-list of tool names; empty = all read tools.
	Enabled []string `yaml:"enabled"`
	// AutoApprove lists risk classes that skip the human gate in apply mode,
	// e.g. ["scale", "rollout-restart"]. Never includes destructive ops.
	AutoApprove []string `yaml:"auto_approve"`
	// MCPServers are external MCP endpoints to mount as tools.
	MCPServers []MCPServer `yaml:"mcp_servers"`
}

// MCPServer describes an external Model Context Protocol server to mount.
type MCPServer struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"` // stdio transport
	Args    []string `yaml:"args"`
	URL     string   `yaml:"url"` // http transport (alternative to command)
}

// ObserveConfig configures metrics/tracing.
type ObserveConfig struct {
	MetricsAddr  string `yaml:"metrics_addr"` // e.g. ":9090"; empty disables
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	LogLevel     string `yaml:"log_level"` // debug|info|warn|error
}

// IncidentConfig configures alert sinks.
type IncidentConfig struct {
	SlackWebhook      string `yaml:"slack_webhook"`        // read from env in practice
	PagerDutyKey      string `yaml:"pagerduty_key"`        // read from env in practice
	EscalateAtOrAbove string `yaml:"escalate_at_or_above"` // P0|P1
}

// Default returns a safe baseline configuration.
func Default() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Mode:    ModeReadOnly,
		DataDir: filepath.Join(home, ".voujr"),
		AI: AIConfig{
			Gateway:         "portkey",
			DefaultProvider: "anthropic",
			Timeout:         90 * time.Second,
			Tiers: map[string]string{
				"fast":      "anthropic/claude-haiku-4-5",
				"reasoning": "anthropic/claude-opus-4-8",
				"long":      "anthropic/claude-sonnet-4-6",
			},
			EmbeddingModel: "openai/text-embedding-3-small",
		},
		Audit:    AuditConfig{Enabled: true, Interval: 0},
		Observe:  ObserveConfig{LogLevel: "info"},
		Incident: IncidentConfig{EscalateAtOrAbove: "P1"},
	}
}

// Load resolves configuration from files then applies env overrides. Flag
// overrides are applied by the caller (cmd layer) after Load.
func Load() (Config, error) {
	cfg := Default()

	// user file then project file (project wins).
	for _, path := range configPaths() {
		if err := mergeFile(&cfg, path); err != nil {
			return cfg, fmt.Errorf("config %s: %w", path, err)
		}
	}
	applyEnv(&cfg)
	return cfg, nil
}

func configPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".voujr", "config.yaml"),
		"voujr.yaml",
	}
}

func mergeFile(cfg *Config, path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil // optional
	}
	if err != nil {
		return err
	}
	return yaml.Unmarshal(b, cfg)
}

func applyEnv(cfg *Config) {
	if v := os.Getenv("VOUJR_MODE"); v != "" {
		cfg.Mode = Mode(v)
	}
	if v := os.Getenv("VOUJR_CONTEXT"); v != "" {
		cfg.Context = v
	}
	if v := os.Getenv("PORTKEY_BASE_URL"); v != "" {
		cfg.AI.BaseURL = v
	}
	if v := os.Getenv("PROMETHEUS_URL"); v != "" {
		cfg.Integrations.PrometheusURL = v
	}
	if v := os.Getenv("VOUJR_METRICS_ADDR"); v != "" {
		cfg.Observe.MetricsAddr = v
	}
	if v := os.Getenv("VOUJR_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("SLACK_WEBHOOK_URL"); v != "" {
		cfg.Incident.SlackWebhook = v
	}
	if v := os.Getenv("PAGERDUTY_ROUTING_KEY"); v != "" {
		cfg.Incident.PagerDutyKey = v
	}
}

// APIKey resolves the provider/gateway key from the environment at call time.
// Keys are never stored in Config or the database.
func (c AIConfig) APIKey() (string, error) {
	switch strings.ToLower(c.Gateway) {
	case "portkey":
		if k := os.Getenv("PORTKEY_API_KEY"); k != "" {
			return k, nil
		}
	}
	// direct provider keys (also used as Portkey virtual-key fallbacks)
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY"} {
		if k := os.Getenv(env); k != "" {
			return k, nil
		}
	}
	return "", errors.New("no API key found: set PORTKEY_API_KEY or a provider key")
}

// Validate enforces invariants. The process must not start in apply mode without
// an audit sink, and tiers must be populated.
func (c Config) Validate() error {
	switch c.Mode {
	case ModeReadOnly, ModePropose, ModeApply:
	default:
		return fmt.Errorf("invalid mode %q", c.Mode)
	}
	if len(c.AI.Tiers) == 0 {
		return errors.New("ai.tiers must define at least one model tier")
	}
	if _, err := c.AI.APIKey(); err != nil {
		return err
	}
	if c.Mode == ModeApply && c.DataDir == "" {
		return errors.New("apply mode requires data_dir for the audit log")
	}
	return nil
}
