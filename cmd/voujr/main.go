// Command voujr is the terminal-native Kubernetes AI copilot. This is the
// composition root: it loads config, connects clusters, wires the AI provider,
// tool registry (with policy/approval/audit/redaction), the agent runtime, and
// the Bubble Tea UI, then runs the interactive loop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/voujr/voujr/internal/agent"
	"github.com/voujr/voujr/internal/ai"
	"github.com/voujr/voujr/internal/audit"
	"github.com/voujr/voujr/internal/audit/rules"
	"github.com/voujr/voujr/internal/config"
	"github.com/voujr/voujr/internal/controller"
	"github.com/voujr/voujr/internal/incident"
	"github.com/voujr/voujr/internal/k8s"
	"github.com/voujr/voujr/internal/observability"
	"github.com/voujr/voujr/internal/security"
	"github.com/voujr/voujr/internal/session"
	"github.com/voujr/voujr/internal/store"
	"github.com/voujr/voujr/internal/tools"
	"github.com/voujr/voujr/internal/tui"
)

// version is set via -ldflags at build time.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	var (
		kubeContext   string
		mode          string
		inCluster     bool
		resumeID      string
		extraClusters []string
	)
	cmd := &cobra.Command{
		Use:     "voujr",
		Short:   "Terminal-native, AI-powered Kubernetes copilot",
		Version: version,
		// main() prints the error itself; don't let cobra double-print it or dump
		// usage on a runtime/validation failure (usage is for flag-parse errors).
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			// Flag overrides (highest precedence).
			if kubeContext != "" {
				cfg.Context = kubeContext
			}
			if mode != "" {
				cfg.Mode = config.Mode(mode)
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			return run(cmd.Context(), cfg, inCluster, resumeID, extraClusters)
		},
	}
	cmd.Flags().StringVar(&kubeContext, "context", "", "kube-context to connect to (default: current-context)")
	cmd.Flags().StringVar(&mode, "mode", "", "authority: read-only | propose | apply")
	cmd.Flags().BoolVar(&inCluster, "in-cluster", false, "use the mounted ServiceAccount instead of kubeconfig")
	cmd.Flags().StringVar(&resumeID, "resume", "", "resume a prior session by id (see `voujr sessions`)")
	cmd.Flags().StringSliceVar(&extraClusters, "clusters", nil, "additional kube-contexts to register (switch in-session with /cluster)")
	cmd.AddCommand(newAuditCmd())
	cmd.AddCommand(newSessionsCmd())
	cmd.AddCommand(newControllerCmd())
	return cmd
}

// newControllerCmd is "team mode": run the audit engine continuously across the
// registered fleet, persisting findings, updating metrics, and firing incidents.
// Intended to run in-cluster (see deploy/helm). Needs no AI key.
func newControllerCmd() *cobra.Command {
	var (
		kubeContext string
		namespace   string
		inCluster   bool
		interval    time.Duration
		extra       []string
	)
	cmd := &cobra.Command{
		Use:           "controller",
		Short:         "Run continuous audit (team mode): scan the fleet on a schedule, persist findings, alert, serve /metrics",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if kubeContext != "" {
				cfg.Context = kubeContext
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			log := observability.NewLogger(cfg.Observe.LogLevel, os.Stderr)
			metrics := observability.NewMetrics()
			observability.Serve(cfg.Observe.MetricsAddr)

			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			clusters, _, err := connectCluster(ctx, cfg, inCluster)
			if err != nil {
				return err
			}
			for _, e := range extra {
				if e == "" || e == cfg.Context {
					continue
				}
				if _, err := clusters.Add(e, e, false); err != nil {
					log.Warn("cluster not registered", "cluster", e, "error", err)
				}
			}

			var alerter controller.Alerter
			if n := incident.NewNotifier(cfg.Incident.SlackWebhook, cfg.Incident.PagerDutyKey,
				audit.Severity(cfg.Incident.EscalateAtOrAbove), &http.Client{Timeout: 10 * time.Second}); n.Enabled() {
				alerter = n
			}

			iv := interval
			if iv <= 0 {
				iv = cfg.Audit.Interval
			}
			ctrl := controller.New(controller.Config{
				Scanner:   newAuditEngine(clusters, cfg),
				Sink:      st,
				Alerter:   alerter,
				Metrics:   metrics,
				Log:       log,
				Namespace: namespace,
				Interval:  iv,
			})

			if err := ctrl.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
			return nil // clean shutdown on signal
		},
	}
	cmd.Flags().StringVar(&kubeContext, "context", "", "kube-context (default: current-context)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to scan (default: all)")
	cmd.Flags().BoolVar(&inCluster, "in-cluster", false, "use the mounted ServiceAccount instead of kubeconfig")
	cmd.Flags().DurationVar(&interval, "interval", 0, "scan interval (default: config audit.interval, else 15m)")
	cmd.Flags().StringSliceVar(&extra, "clusters", nil, "additional kube-contexts to include in the fleet scan")
	return cmd
}

// newSessionsCmd lists recent sessions so the operator can find an id to --resume.
func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "sessions",
		Short:         "List recent sessions (use an id with --resume)",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			list, err := st.ListSessions(cmd.Context(), 20)
			if err != nil {
				return err
			}
			if len(list) == 0 {
				fmt.Println("no sessions yet")
				return nil
			}
			fmt.Printf("%-36s  %-19s  %-10s  %4s  %s\n", "SESSION", "CREATED", "MODE", "MSGS", "CLUSTER")
			for _, s := range list {
				fmt.Printf("%-36s  %-19s  %-10s  %4d  %s\n", s.ID, s.CreatedAt, s.Mode, s.Messages, s.Cluster)
			}
			return nil
		},
	}
	return cmd
}

// newAuditCmd is the non-interactive `voujr audit` subcommand: run the audit
// engine, persist findings, and print a report. It needs no AI key.
func newAuditCmd() *cobra.Command {
	var (
		kubeContext string
		namespace   string
		inCluster   bool
		asJSON      bool
	)
	cmd := &cobra.Command{
		Use:           "audit",
		Short:         "Scan the cluster for reliability, security, cost, and optimization issues",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if kubeContext != "" {
				cfg.Context = kubeContext
			}
			// Audit needs no model, so we deliberately skip cfg.Validate() (which
			// would demand an AI key).
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer func() { _ = st.Close() }()

			clusters, cluster, err := connectCluster(ctx, cfg, inCluster)
			if err != nil {
				return err
			}
			rep, err := newAuditEngine(clusters, cfg).Scan(ctx, cluster, namespace)
			if err != nil {
				return fmt.Errorf("scan: %w", err)
			}

			// Persist findings (upsert + dedup by rule+resource).
			clusterID, _ := st.UpsertCluster(ctx, cluster.Name, cfg.Context, "")
			for _, f := range rep.Findings {
				_ = st.SaveFinding(ctx, clusterID, f)
			}

			log := observability.NewLogger(cfg.Observe.LogLevel, os.Stderr)
			counts := rep.BySeverity()
			log.Info("audit complete",
				"cluster", cluster.Name, "findings", len(rep.Findings),
				"p0", counts[audit.P0], "p1", counts[audit.P1], "duration", rep.Duration)

			// Alert on high-severity findings if a sink (Slack/PagerDuty) is configured.
			notifier := incident.NewNotifier(cfg.Incident.SlackWebhook, cfg.Incident.PagerDutyKey,
				audit.Severity(cfg.Incident.EscalateAtOrAbove), &http.Client{Timeout: 10 * time.Second})
			if notifier.Enabled() {
				if fired, err := notifier.Notify(ctx, rep.Findings); err != nil {
					log.Warn("incident notify failed", "error", err)
				} else if fired > 0 {
					log.Info("incidents alerted", "count", fired, "threshold", cfg.Incident.EscalateAtOrAbove)
				}
			}

			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(rep.Findings)
			}
			printReport(cluster.Name, namespace, rep)
			return nil
		},
	}
	cmd.Flags().StringVar(&kubeContext, "context", "", "kube-context to scan (default: current-context)")
	cmd.Flags().StringVarP(&namespace, "namespace", "n", "", "namespace to scan (default: all)")
	cmd.Flags().BoolVar(&inCluster, "in-cluster", false, "use the mounted ServiceAccount instead of kubeconfig")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit findings as JSON")
	return cmd
}

func run(ctx context.Context, cfg config.Config, inCluster bool, resumeID string, extraClusters []string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Observability: Prometheus collectors (+ /metrics endpoint when configured).
	metrics := observability.NewMetrics()
	observability.Serve(cfg.Observe.MetricsAddr)

	// Persistence: local SQLite at <data_dir>/state.db (pure-Go driver, no cgo).
	st, err := openStore(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	// Resume: adopt the prior session's cluster/mode/tools and reload its history.
	var resumed []ai.Message
	var sessionID string
	if resumeID != "" {
		info, err := st.GetSession(ctx, resumeID)
		if err != nil {
			return fmt.Errorf("resume session %s: %w", resumeID, err)
		}
		sessionID = info.ID
		if cfg.Context == "" {
			cfg.Context = info.ClusterContext
		}
		cfg.Mode = config.Mode(info.Mode)
		if len(cfg.Tools.Enabled) == 0 {
			cfg.Tools.Enabled = info.EnabledTools
		}
		if resumed, err = st.LoadMessages(ctx, sessionID); err != nil {
			return fmt.Errorf("load history: %w", err)
		}
	}

	// Clusters: primary (active) plus any extras registered for /cluster switching.
	clusters, cluster, err := connectCluster(ctx, cfg, inCluster)
	if err != nil {
		return err
	}
	for _, extra := range extraClusters {
		if extra == "" || extra == cluster.Name {
			continue
		}
		if _, err := clusters.Add(extra, extra, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cluster %q not registered: %v\n", extra, err)
		}
	}

	// For a fresh session, bootstrap the persisted identity/cluster/session rows so
	// executions and messages have a home (FKs are satisfied before anything writes).
	if sessionID == "" {
		userID, err := st.EnsureUser(ctx, osUsername(), "", "admin")
		if err != nil {
			return fmt.Errorf("ensure user: %w", err)
		}
		clusterID, err := st.UpsertCluster(ctx, cluster.Name, cfg.Context, "")
		if err != nil {
			return fmt.Errorf("upsert cluster: %w", err)
		}
		sessionID, err = st.CreateSession(ctx, userID, clusterID, string(cfg.Mode), cfg.Tools.Enabled, cfg.AI.BudgetCents)
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
	}

	// AI provider (Portkey gateway) + router.
	apiKey, err := cfg.AI.APIKey()
	if err != nil {
		return err
	}
	provider := ai.NewPortkey(cfg.AI.BaseURL, apiKey, cfg.AI.EmbeddingModel, cfg.AI.Timeout, modelCatalog(cfg))
	router := ai.NewRouter(cfg.AI.Tiers)

	// Long-term memory: embed-and-recall over the store. Recall spans all sessions
	// (cross-session memory); new facts are tagged with the current session.
	memory := &memoryStore{store: st, embedder: provider, sessionID: sessionID}

	// UI is constructed first so it can serve as the tool Approver (see SetAgent).
	ui := tui.New(ctx, nil, clusters)
	if resumeID != "" {
		ui.Notice(fmt.Sprintf("Resumed session — %d prior messages restored as context.\n", len(resumed)))
	}
	ui.Notice(fmt.Sprintf("session: %s  (resume later with --resume %s)\n", sessionID, sessionID))

	// Tool registry with the full safety chain. The store is the audit sink:
	// every execution lands in tool_executions plus the hash-chained audit_log.
	redactor := security.NewRedactor()
	policy := security.DefaultPolicy()
	registry := tools.NewRegistry(policy, ui, st, redactor)
	registry.SetObserver(metricsObserver{metrics})
	// Read tools — observation & investigation.
	registry.Register(tools.KubectlGet{Clusters: clusters})
	registry.Register(tools.KubectlDescribe{Clusters: clusters})
	registry.Register(tools.KubectlLogs{Clusters: clusters})
	registry.Register(tools.KubectlEvents{Clusters: clusters})
	registry.Register(tools.AuditScan{Clusters: clusters, Engine: newAuditEngine(clusters, cfg)})
	registry.Register(tools.Remember{Sink: memory})
	// Mutating tools — gated by the approval chain.
	registry.Register(tools.KubectlScale{Clusters: clusters})
	registry.Register(tools.KubectlRolloutRestart{Clusters: clusters})
	// Prometheus is only advertised when an endpoint is configured.
	if cfg.Integrations.PrometheusURL != "" {
		registry.Register(tools.PrometheusQuery{
			BaseURL: cfg.Integrations.PrometheusURL,
			HTTP:    &http.Client{Timeout: 15 * time.Second},
		})
	}

	// Agent runtime. Conversation messages and token/cost usage are persisted.
	a := agent.New(agent.Config{
		Provider: provider,
		Router:   router,
		Registry: registry,
		Clusters: clusters,
		Session: tools.SessionPolicy{
			SessionID:   sessionID,
			Mode:        string(cfg.Mode),
			Enabled:     cfg.Tools.Enabled,
			Cluster:     cluster.Name,
			DryRunFirst: true,
		},
		BudgetCents: cfg.AI.BudgetCents,
		Persist: func(ctx context.Context, m ai.Message) error {
			return st.AppendMessage(ctx, sessionID, m)
		},
		RecordUsage: func(ctx context.Context, u ai.Usage, reason string) error {
			metrics.RecordAIUsage(provider.Name(), u.Model, u.InputTokens, u.OutputTokens, u.CostCents)
			return st.RecordUsage(ctx, sessionID, provider.Name(), u, reason)
		},
		Memory: memory,
		OnTurn: metrics.RecordTurn,
	})
	if len(resumed) > 0 {
		a.Restore(resumed)
	}
	ui.SetAgent(a)

	// Run the interactive UI.
	p := tea.NewProgram(ui, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err = p.Run()
	return err
}

// modelCatalog declares the models the router can choose from, with pricing for
// cost estimation. In production this is loaded from config / a pricing service.
func modelCatalog(cfg config.Config) []ai.ModelInfo {
	return []ai.ModelInfo{
		{Ref: "anthropic/claude-haiku-4-5", Tier: "fast", ContextWindow: 200000, InputCentsPerMTok: 100, OutputCentsPerMTok: 500},
		{Ref: "anthropic/claude-sonnet-4-6", Tier: "long", ContextWindow: 1000000, InputCentsPerMTok: 300, OutputCentsPerMTok: 1500},
		{Ref: "anthropic/claude-opus-4-8", Tier: "reasoning", ContextWindow: 200000, InputCentsPerMTok: 1500, OutputCentsPerMTok: 7500},
		{Ref: "openai/gpt-5", Tier: "reasoning", ContextWindow: 400000, InputCentsPerMTok: 1250, OutputCentsPerMTok: 10000},
		{Ref: "gemini/gemini-2.5-pro", Tier: "long", ContextWindow: 2000000, InputCentsPerMTok: 125, OutputCentsPerMTok: 1000},
	}
}

// memoryStore is the composition-root adapter that turns the store + an embedding
// model into long-term memory. It implements agent.Memory (Recall) and
// tools.MemorySink (Remember). Recall spans all sessions; Remember tags the
// current session. Embedding failures degrade gracefully (recall yields nothing).
type memoryStore struct {
	store     store.Store
	embedder  ai.Provider
	sessionID string
}

func (m *memoryStore) Recall(ctx context.Context, query string, k int) ([]string, error) {
	embs, err := m.embedder.Embed(ctx, []string{query})
	if err != nil || len(embs) == 0 {
		return nil, err
	}
	mems, err := m.store.RecallMemories(ctx, "", embs[0], k) // "" = across all sessions
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(mems))
	for _, mm := range mems {
		out = append(out, mm.Text)
	}
	return out, nil
}

func (m *memoryStore) Remember(ctx context.Context, kind, text string) error {
	var vec []float32
	if embs, err := m.embedder.Embed(ctx, []string{text}); err == nil && len(embs) > 0 {
		vec = embs[0]
	}
	return m.store.SaveMemory(ctx, session.Memory{
		SessionID: m.sessionID, Kind: kind, Text: text, Embedding: vec,
	})
}

// metricsObserver adapts the Prometheus collector set to the tools.Observer
// interface so the dispatch chain stays decoupled from the metrics package.
type metricsObserver struct{ m *observability.Metrics }

func (o metricsObserver) ToolExecuted(tool, status string, d time.Duration) {
	o.m.RecordToolExec(tool, status, d)
}
func (o metricsObserver) ApprovalDecided(approved bool) { o.m.RecordApproval(approved) }

// osUsername identifies the local operator for the audit trail, falling back to
// "local" when the OS user can't be resolved.
func osUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "local"
}

// openStore opens the configured persistence backend (SQLite by default,
// Postgres when store.driver=postgres). Applied in one place so every command
// path (run/audit/sessions/controller) is consistent.
func openStore(cfg config.Config) (store.Store, error) {
	var st store.Store
	switch strings.ToLower(cfg.Store.Driver) {
	case "postgres", "postgresql", "pgx":
		if cfg.Store.DSN == "" {
			return nil, fmt.Errorf("postgres driver requires store.dsn (or DATABASE_URL)")
		}
		ps, err := store.OpenPostgres(cfg.Store.DSN)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		st = ps
	default: // sqlite
		if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
		ls, err := store.OpenSQLite(filepath.Join(cfg.DataDir, "state.db"))
		if err != nil {
			return nil, fmt.Errorf("open store: %w", err)
		}
		st = ls
	}

	// Encryption at rest for sensitive columns when VOUJR_DB_KEY is set.
	if key := os.Getenv("VOUJR_DB_KEY"); key != "" {
		cipher, err := security.NewCipher(security.DeriveKey(key))
		if err != nil {
			_ = st.Close()
			return nil, fmt.Errorf("db encryption: %w", err)
		}
		st.SetCipher(cipher)
	}
	return st, nil
}

// connectCluster builds the cluster registry, connects the active cluster, and
// verifies it is reachable.
func connectCluster(ctx context.Context, cfg config.Config, inCluster bool) (*k8s.Registry, *k8s.Cluster, error) {
	clusters := k8s.NewRegistry()
	cluster, err := clusters.Add(cfg.Context, cfg.Context, inCluster)
	if err != nil {
		return nil, nil, fmt.Errorf("connect cluster: %w", err)
	}
	if err := cluster.Health(ctx); err != nil {
		return nil, nil, fmt.Errorf("cluster unreachable: %w", err)
	}
	return clusters, cluster, nil
}

// newAuditEngine builds the audit engine with the full built-in rule library.
func newAuditEngine(clusters *k8s.Registry, cfg config.Config) *audit.Engine {
	rs := audit.NewRuleSet()
	rules.RegisterAll(rs)
	return audit.NewEngine(clusters, rs, cfg.Audit.DisabledRules)
}

// printReport renders an audit report to stdout for the `voujr audit` command.
func printReport(cluster, namespace string, rep audit.Report) {
	scope := namespace
	if scope == "" {
		scope = "all namespaces"
	}
	c := rep.BySeverity()
	fmt.Printf("voujr audit — %s / %s\n", cluster, scope)
	fmt.Printf("%d findings: P0=%d  P1=%d  P2=%d  P3=%d  (%s)\n\n",
		len(rep.Findings), c[audit.P0], c[audit.P1], c[audit.P2], c[audit.P3], rep.Duration.Round(1e6))
	if len(rep.Findings) == 0 {
		fmt.Println("  no issues found ✓")
		return
	}
	for _, f := range rep.Findings {
		fmt.Printf("  [%s] %-13s %s/%s\n", f.Severity, f.Category, f.Resource.Namespace, f.Resource.Name)
		fmt.Printf("        %s\n", f.Title)
		fmt.Printf("        fix: %s\n", f.Remediation.Summary)
		if f.Remediation.KubectlEquivalent != "" {
			fmt.Printf("        $ %s\n", f.Remediation.KubectlEquivalent)
		}
		fmt.Println()
	}
}
