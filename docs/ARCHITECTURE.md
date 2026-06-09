# voujr — Architecture

A terminal-native, AI-powered Kubernetes copilot. This document is the system design
reference. It maps 1:1 to the 15 architecture deliverables:

1. [Production architecture diagram](#1-production-architecture-diagram)
2. [Agent runtime design](#2-agent-runtime-design)
3. [Go project structure](#3-go-project-structure)
4. [MCP-style tool architecture](#4-mcp-style-tool-architecture)
5. [AI orchestration layer](#5-ai-orchestration-layer)
6. [Kubernetes integration layer](#6-kubernetes-integration-layer)
7. [Memory & session architecture](#7-memory--session-architecture)
8. [Database schema](#8-database-schema)
9. [Security model](#9-security-model)
10. [RBAC strategy](#10-rbac-strategy)
11. [Multi-cluster support design](#11-multi-cluster-support-design)
12. [Scalability considerations](#12-scalability-considerations)
13. [Example Go implementations](#13-example-go-implementations)
14. [Deployment strategy](#14-deployment-strategy)
15. [Trade-off analysis vs. Claude Code and kubectl](#15-trade-off-analysis)

---

## Design principles

These constrain every decision below.

- **Read-by-default, write-by-approval.** The agent can observe freely; every mutation
  passes a policy check + human approval + dry-run before it touches a cluster.
- **The LLM proposes; deterministic code disposes.** Models choose *which* tool and
  *what* arguments. Validation, RBAC, diffing, execution, and rollback are plain Go that
  never trusts model output blindly.
- **Tools are the only side effects.** All cluster/cloud interaction goes through the
  tool registry. There is no hidden path from a prompt to `kubectl apply`.
- **Provider-agnostic.** No business logic depends on a specific LLM vendor. One
  `Provider` interface; Portkey is the default gateway, but a direct SDK adapter works too.
- **Local-first, server-optional.** Ships as a single binary an engineer runs on their
  laptop against their kube-context. The same core runs as an in-cluster controller for
  continuous audit and team features.
- **Everything is observable and auditable.** Metrics, traces, and an append-only audit
  log are first-class, not bolted on.

---

## 1. Production architecture diagram

### 1.1 Component view

```
┌──────────────────────────────────────────────────────────────────────────────┐
│                                  OPERATOR                                       │
│                              (terminal / TTY)                                   │
└───────────────────────────────────┬────────────────────────────────────────────┘
                                     │ keystrokes / streamed tokens
                          ┌──────────▼───────────┐
                          │   TUI  (Bubble Tea)   │  chat · tables · trees · logs
                          │   internal/tui        │  progress · multi-pane · vim keys
                          └──────────┬───────────┘
                                     │ commands / events (Go channels)
┌────────────────────────────────────▼───────────────────────────────────────────┐
│                              AGENT RUNTIME                                        │
│                              internal/agent                                       │
│   ┌────────────┐   ┌──────────────┐   ┌───────────────┐   ┌──────────────────┐   │
│   │  Planner   │──▶│  Reasoner    │──▶│ Tool dispatch │──▶│  Observation /    │   │
│   │  (intent,  │   │  (LLM turn)  │   │ (validate +   │   │  result merge     │   │
│   │  routing)  │◀──│              │◀──│  approve +    │◀──│  → next turn)     │   │
│   └────────────┘   └──────┬───────┘   │  execute)     │   └──────────────────┘   │
│                           │           └───────┬───────┘                          │
└───────────────────────────┼───────────────────┼─────────────────────────────────┘
            ┌───────────────┘                   │                  ┌───────────────┐
            ▼                                    ▼                  ▼               │
┌────────────────────┐        ┌─────────────────────────┐  ┌───────────────────┐   │
│  AI ORCHESTRATION   │        │     TOOL REGISTRY        │  │  SESSION / MEMORY │   │
│  internal/ai        │        │     internal/tools       │  │  internal/session │   │
│  ┌───────────────┐  │        │  policy → approval →     │  │  short-term buffer│   │
│  │ Model router  │  │        │  dry-run → exec → audit  │  │  long-term recall │   │
│  │ cost/ctx/skill│  │        │  ┌────────┬───────────┐  │  │  summarization    │   │
│  └──────┬────────┘  │        │  │kubectl │ helm      │  │  └─────────┬─────────┘   │
│  ┌──────▼────────┐  │        │  │prom    │ argocd    │  │            │             │
│  │ Portkey gw    │  │        │  │loki    │ terraform │  │            ▼             │
│  │ failover/BYOK │  │        │  │grafana │ aws/gcp/az│  │   ┌─────────────────┐   │
│  └──────┬────────┘  │        │  └────┬───┴─────┬─────┘  │   │  STORE (DB)      │   │
└─────────┼───────────┘        └───────┼─────────┼────────┘   │  internal/store  │   │
          │                            │         │            │  SQLite│Postgres │   │
   ┌──────▼──────┐              ┌──────▼─────┐   │            └────────┬─────────┘   │
   │ OpenAI       │             │ KUBERNETES │   │                     │             │
   │ Anthropic    │             │ INTEGRATION│   │             sessions, messages,   │
   │ Gemini       │             │ internal/k8s│  │             tool_exec, findings,  │
   └─────────────┘              │ client-go   │  │             clusters, approvals,  │
                                │ informers   │  │             ai_usage, audit_log   │
                                │ multi-cluster│ │                                   │
                                └──────┬──────┘  │                                   │
                                       │         │                                   │
                          ┌────────────▼─────────▼───────────┐                       │
                          │          AUDIT ENGINE             │  rules → findings     │
                          │          internal/audit          │  reliability/security │
                          │  scheduled + on-demand scans      │  cost/optimization    │
                          └────────────┬──────────────────────┘                       │
                                       │ P0/P1                                         │
                          ┌────────────▼──────────┐   ┌──────────────────────────────┐│
                          │  INCIDENT / ALERTING   │   │  OBSERVABILITY                ││
                          │  internal/incident     │   │  internal/observability       ││
                          │  Slack · PagerDuty      │   │  Prometheus · OTel · logs     ││
                          └────────────────────────┘   └──────────────────────────────┘│
                                                                                        │
              ┌─────────────────────────────────────────────────────────────────────────┘
              ▼  targets (read-by-default; writes gated)
   ┌──────────────────────────────────────────────────────────────────────────┐
   │  Cluster A (EKS)   Cluster B (GKE)   Cluster C (AKS)   …   Prometheus/Loki │
   └──────────────────────────────────────────────────────────────────────────┘
```

### 1.2 Deployment topologies

**A. Local CLI (default).** One binary on the operator's machine. Uses their
`~/.kube/config` contexts and their own RBAC. State in a local SQLite file
(`~/.voujr/state.db`). BYOK from env/keyring. Zero server infrastructure.

**B. In-cluster controller (team mode).** The same binary runs as a Deployment with a
scoped ServiceAccount. Runs continuous audit, persists to Postgres, exposes a gRPC/SSH
endpoint that thin CLI clients attach to. Adds SSO, team RBAC, shared findings, and
fleet-wide dashboards.

**C. Hybrid.** Local CLI for interactive work + an in-cluster controller for 24/7 audit
and alerting. The CLI reads the controller's findings over its API.

---

## 2. Agent runtime design

The runtime is a bounded **ReAct loop** (Reason → Act → Observe) with explicit phases,
hard step/budget limits, and a human-in-the-loop gate on every mutation.

### 2.1 The loop

```
                    ┌──────────────────────────────────────────────┐
   user message ──▶ │  1. CLASSIFY      intent, complexity, scope    │
                    │     → route to a model tier (§5)               │
                    └───────────────────┬──────────────────────────┘
                                        ▼
                    ┌──────────────────────────────────────────────┐
                    │  2. ASSEMBLE      system prompt + cluster      │
                    │     context snapshot + tool schemas +          │
                    │     conversation window + recalled memory      │
                    └───────────────────┬──────────────────────────┘
                                        ▼
          ┌────────────▶┌──────────────────────────────────────────┐
          │             │  3. REASON    LLM turn (streamed)          │
          │             │     emits: assistant text  AND/OR          │
          │             │     one or more tool calls                 │
          │             └───────────────┬──────────────────────────┘
          │                             │ tool calls?
          │                   ┌─────────┴─────────┐
          │                  no                   yes
          │                   │                     ▼
          │                   │     ┌──────────────────────────────────┐
          │                   │     │  4. VALIDATE   args vs JSON schema │
          │                   │     │     + policy check (mutating?      │
          │                   │     │     RBAC? blast radius?)           │
          │                   │     └───────────────┬──────────────────┘
          │                   │                     ▼
          │                   │     ┌──────────────────────────────────┐
          │                   │     │  5. APPROVE   if mutating:         │
          │                   │     │     dry-run → diff → human y/N     │
          │                   │     │     (auto-approved if --yes +      │
          │                   │     │      policy allows)                │
          │                   │     └───────────────┬──────────────────┘
          │                   │                     ▼
          │                   │     ┌──────────────────────────────────┐
          │                   │     │  6. EXECUTE   run tool, capture    │
          │                   │     │     result + snapshot for rollback │
          │                   │     │     + append to audit log          │
          │                   │     └───────────────┬──────────────────┘
          │                   │                     ▼
          │                   │     ┌──────────────────────────────────┐
          │                   │     │  7. OBSERVE   feed tool result(s)  │
          │                   └─────┤     back as a tool message; loop   │
          │   step budget ok? ◀─────┤     until no tool calls or budget  │
          └─────────────────────────┤     exhausted                      │
                                    └───────────────┬──────────────────┘
                                                    ▼
                    ┌──────────────────────────────────────────────┐
                    │  8. FINALIZE   stream final answer, persist    │
                    │     turn, update long-term memory if salient   │
                    └──────────────────────────────────────────────┘
```

### 2.2 Guardrails baked into the loop

- **Step budget.** `maxSteps` (default 12) and a token/cost budget per turn. On
  exhaustion the agent summarizes progress and asks how to proceed rather than looping.
- **Tool allow-list per session.** A session is created with an explicit set of enabled
  tools and a mode (`read-only` | `propose` | `apply`). Even if the model hallucinates a
  tool call, dispatch rejects anything not enabled.
- **Mutation gate.** Any tool flagged `Mutating` cannot execute without passing
  approval. In `read-only` mode, mutating tools aren't even advertised to the model.
- **Idempotency & cancellation.** Every step runs under a `context.Context` with timeout;
  the user can `Ctrl-C` to cancel an in-flight tool or model stream cleanly.
- **Determinism boundary.** The model never receives raw kubeconfig, tokens, or secret
  values. Tool results are post-processed to redact secrets before re-entering the prompt.

### 2.3 Concurrency model

- The runtime is single-goroutine per **turn** (sequential reasoning) but tools may fan
  out internally (e.g. an audit scan queries many resources concurrently with
  `errgroup`).
- The TUI runs on its own goroutine; the runtime communicates via channels
  (`tea.Msg`): token deltas, tool-start/finish events, approval requests, final answer.
- Multiple sessions (server mode) are independent goroutines coordinated by a session
  manager; each owns its own context window and cluster handle.

### 2.4 Prompt assembly

The system prompt is composed from layered fragments so it stays cache-friendly:

```
[ stable system preamble ]        ← role, safety rules, output conventions (cacheable)
[ tool schemas ]                  ← JSON schema of enabled tools (changes rarely)
[ cluster context card ]          ← compact snapshot: cluster, namespaces, key counts
[ recalled long-term memory ]     ← top-k relevant prior facts/decisions
[ conversation window ]           ← last N turns, older turns summarized
[ current user message ]
```

Stable prefixes are placed first to maximize provider-side prompt caching (Anthropic
prompt caching / OpenAI automatic caching), cutting cost and latency on multi-step loops.

---

## 3. Go project structure

```
voujr/
├── cmd/
│   └── voujr/
│       └── main.go                # Cobra CLI, flag parsing, DI wiring, run loop
├── internal/
│   ├── agent/                     # the runtime
│   │   ├── agent.go               # Agent struct, Run(ctx, msg)
│   │   ├── loop.go                # ReAct step loop
│   │   ├── planner.go             # intent classification → routing hints
│   │   └── prompt.go              # system prompt assembly + context cards
│   ├── ai/                        # AI orchestration
│   │   ├── provider.go            # Provider interface, Message/ToolCall types
│   │   ├── portkey.go             # Portkey gateway adapter (unified API)
│   │   ├── anthropic.go           # direct Anthropic adapter (fallback path)
│   │   ├── router.go              # model selection (cost/ctx/skill)
│   │   └── stream.go              # streaming + token accounting
│   ├── tools/                     # MCP-style tools
│   │   ├── tool.go                # Tool interface, Result, RiskLevel
│   │   ├── registry.go            # registration + schema generation + dispatch
│   │   ├── approval.go            # approval + dry-run + rollback middleware
│   │   ├── kubectl.go             # read/apply against the cluster
│   │   ├── helm.go                # helm list/diff/upgrade
│   │   ├── prometheus.go          # PromQL queries
│   │   ├── loki.go                # log queries
│   │   ├── argocd.go              # app sync/status
│   │   └── cloud/                 # aws.go, gcp.go, azure.go cost/inventory
│   ├── k8s/                       # cluster integration
│   │   ├── client.go              # typed + dynamic clients, discovery
│   │   ├── multicluster.go        # cluster registry, context switching
│   │   ├── snapshot.go            # live-state cache via informers
│   │   └── metrics.go             # metrics-server / VPA recommendations
│   ├── audit/                     # audit engine
│   │   ├── engine.go              # scan orchestration, scheduling
│   │   ├── finding.go             # Finding model, severity, remediation
│   │   ├── rule.go                # Rule interface + registry
│   │   └── rules/                 # reliability/, security/, cost/, optimization/
│   ├── session/                   # memory & sessions
│   │   ├── session.go             # Session aggregate
│   │   ├── memory.go              # short-term window + long-term recall
│   │   └── summarize.go           # rolling summarization
│   ├── store/                     # persistence
│   │   ├── store.go               # interface
│   │   ├── sqlite.go              # local driver
│   │   └── postgres.go            # server driver
│   ├── tui/                       # terminal UI
│   │   ├── app.go                 # root Bubble Tea model
│   │   ├── chat.go                # chat viewport + input
│   │   ├── approval.go            # approval modal
│   │   ├── tables.go              # resource tables / trees
│   │   └── styles.go              # lipgloss theme
│   ├── observability/
│   │   ├── metrics.go             # Prometheus collectors
│   │   ├── tracing.go             # OTel setup
│   │   └── logging.go             # structured slog
│   ├── incident/
│   │   ├── slack.go
│   │   └── pagerduty.go
│   ├── security/
│   │   ├── policy.go              # mutation policy, blast-radius rules
│   │   ├── redact.go              # secret redaction
│   │   └── rbac.go                # preflight SelfSubjectAccessReview
│   └── config/
│       └── config.go              # layered config (flags/env/file), validation
├── migrations/
│   ├── 0001_init.sql
│   └── 0002_audit.sql
├── deploy/
│   ├── Dockerfile
│   ├── helm/                      # in-cluster controller chart
│   └── rbac/                      # read-only + write Role/ClusterRole manifests
├── docs/
│   ├── ARCHITECTURE.md
│   └── SECURITY.md
├── go.mod
├── Makefile
└── README.md
```

Rationale: everything is under `internal/` so the public surface is the CLI only. Each
subsystem is a package with a narrow interface; the `cmd` layer wires concrete
implementations together (manual DI — no framework). Adapters (providers, stores, cloud
clients) sit behind interfaces so they're swappable and mockable.

---

## 4. MCP-style tool architecture

Tools are the agent's hands. The model can only affect the world by calling a registered
tool; everything else is reasoning. The design mirrors the **Model Context Protocol**
shape (a tool = name + JSON schema + handler) so tools can be exposed *to* the agent and
also *served* over MCP to other clients.

### 4.1 The contract

```go
type Tool interface {
    Name() string                     // stable identifier, e.g. "kubectl_get"
    Description() string              // model-facing description
    Schema() jsonschema.Schema        // argument schema (validated before exec)
    Risk() RiskLevel                  // Read | Mutate | Destructive
    Mutating() bool                   // gates approval + read-only mode
    Execute(ctx, RawArgs) (Result, error)
}
```

A `Result` carries structured data, a human-readable summary, and a model-facing
rendering (often trimmed/redacted) so big outputs don't blow the context window.

### 4.2 Dispatch pipeline (middleware chain)

Every tool call flows through a composable chain before the concrete handler runs:

```
ToolCall
   │
   ▼  schema-validate        reject malformed args from the model
   ▼  allow-list             reject tools not enabled for this session
   ▼  redact-inputs          strip anything secret-shaped from args
   ▼  policy                 classify blast radius; deny disallowed mutations
   ▼  rbac-preflight         SelfSubjectAccessReview: can the caller actually do this?
   ▼  dry-run  (if mutating) server-side dry-run → compute diff
   ▼  approval (if mutating) present diff to human; wait y/N (or auto per policy)
   ▼  snapshot (if mutating) capture prior object state for rollback
   ▼  execute                run the handler under a timeout
   ▼  audit                  append {args, diff, approver, result, duration} to log
   ▼  redact-output          strip secrets from result before it re-enters the prompt
   ▼
Result
```

This is the single chokepoint where safety is enforced — there is no way to reach
`execute` that skips policy/approval/audit.

### 4.3 Tool catalog (initial)

| Tool | Risk | Notes |
|------|------|-------|
| `kubectl_get` / `kubectl_describe` / `kubectl_logs` / `kubectl_events` | Read | core observation |
| `kubectl_top` | Read | live CPU/mem from metrics-server |
| `kubectl_apply` / `kubectl_patch` / `kubectl_scale` / `kubectl_rollout` | Mutate | gated |
| `kubectl_delete` | Destructive | gated + extra confirmation |
| `helm_list` / `helm_diff` | Read | release inventory + upgrade preview |
| `helm_upgrade` / `helm_rollback` | Mutate | gated |
| `prometheus_query` / `prometheus_range` | Read | PromQL for metrics-backed analysis |
| `loki_query` | Read | log search |
| `grafana_render` | Read | dashboard snapshot links |
| `argocd_app_status` | Read | GitOps drift |
| `argocd_app_sync` | Mutate | gated |
| `terraform_plan` | Read | infra drift preview |
| `terraform_apply` | Destructive | gated, opt-in only |
| `aws_cost` / `gcp_cost` / `azure_cost` | Read | cloud billing for cost analysis |

### 4.4 Tools as MCP servers (extensibility)

The registry can mount external **MCP servers** (stdio or HTTP) and surface their tools
alongside native ones. This is how third parties extend the agent without forking it:
point it at an MCP server for your internal platform API and its tools appear in the
catalog, subject to the same policy/approval/audit chain. Conversely, voujr can
*serve* its read tools over MCP so other assistants can consume cluster state safely.

---

## 5. AI orchestration layer

### 5.1 Provider abstraction

One interface decouples the runtime from any vendor:

```go
type Provider interface {
    Chat(ctx, Request) (Response, error)
    Stream(ctx, Request) (Stream, error)        // token deltas + tool-call deltas
    Embed(ctx, []string) ([][]float32, error)   // for long-term memory recall
    Name() string
    Models() []ModelInfo                          // ids, context window, $/Mtok, skills
}
```

`Request` carries messages, tool schemas, temperature, max tokens, and a `Routing` hint.
`Response`/`Stream` normalize provider-specific shapes into a common `Message` with
optional `ToolCall`s.

### 5.2 Portkey as the default gateway

[Portkey](https://portkey.ai) is a unified LLM gateway: one API in front of OpenAI,
Anthropic, Gemini, and ~all major providers, with built-in retries, fallbacks, load
balancing, caching, observability, and virtual keys for BYOK. We use it as the default
`Provider` implementation so we get multi-provider + failover + cost tracking without
maintaining N SDK integrations.

```
            ┌──────────────────────────────────────────────┐
   agent ──▶│  ai.Router  →  Provider (Portkey adapter)      │
            │                    │                            │
            │              ┌─────▼──────┐  config-as-headers  │
            │              │ Portkey GW │  x-portkey-config:  │
            │              │            │  { strategy:fallback,│
            │              │            │    targets:[…] }     │
            │              └─────┬──────┘                     │
            └────────────────────┼────────────────────────────┘
                  ┌──────────────┼──────────────┐
                  ▼              ▼               ▼
              OpenAI        Anthropic         Gemini
```

Portkey routing strategies are expressed as a *config* (sent as a header or referenced by
id): `fallback` (try A, then B on failure), `loadbalance` (weighted), and `conditional`
(route by metadata). BYOK uses Portkey **virtual keys** so raw provider keys never sit in
our config. A **direct adapter** (`anthropic.go`) exists as a break-glass path if the
gateway is unavailable.

### 5.3 Model router

Routing is a deterministic policy over a classification of the request, *then* the chosen
provider/gateway handles transport-level failover.

```go
type Decision struct {
    Primary   ModelRef   // e.g. anthropic/claude-opus-4-8
    Fallbacks []ModelRef // ordered
    MaxTokens int
    Reason    string
}

func (r *Router) Route(req Classified) Decision
```

| Signal | Routing effect |
|--------|----------------|
| **Complexity** = trivial (status lookup, formatting) | cheap/fast tier (e.g. Haiku / GPT-mini / Flash) |
| **Complexity** = investigation / multi-step reasoning | strong reasoning tier (Opus / GPT-class / Pro) |
| **Estimated context** > tier window | long-context-capable model |
| **Task = embeddings** | dedicated embedding model |
| **Budget pressure** (session cost cap) | downgrade tier, warn user |
| **Provider outage** (from health signals) | reorder fallbacks |

Complexity is estimated cheaply: heuristic features (verbs like *investigate/why/fix* vs
*show/list/get*, number of resources in scope, presence of tool-loop history) optionally
refined by a tiny classifier call. The router records its decision + reason for every turn
(surfaced in `--verbose` and stored in `ai_usage`).

### 5.4 Streaming, sessions, memory, failover — summary

- **Streaming.** `Stream()` yields token deltas and partial tool-call args; the TUI
  renders tokens live and shows a spinner during tool execution.
- **Session persistence.** Conversations, routing decisions, and token/cost usage persist
  to the store (§7, §8); a session resumes with full context after a restart.
- **Failover.** Two layers: gateway-level (Portkey fallback config) for transport errors,
  and router-level (reorder model tiers) for capability/budget reasons.
- **Cost-aware selection.** Per-session and per-user budgets; the router downgrades tiers
  and the UI shows running spend.

---

## 6. Kubernetes integration layer

### 6.1 Clients

Built on `client-go`. Each cluster handle exposes:

- **Typed clientset** for core/apps/batch/etc. (ergonomic, schema-checked).
- **Dynamic client + RESTMapper** for arbitrary CRDs — essential because the agent must
  reason about resources it wasn't compiled against (e.g. `Rollout`, `ScaledObject`).
- **Discovery client** to enumerate available API groups/versions per cluster.
- **metrics.k8s.io** client for live `kubectl top` data and right-sizing.

### 6.2 Live state — informers + snapshot cache

For interactive latency and to avoid hammering the API server, the integration layer can
run **shared informers** over the resource kinds the agent cares about (pods, deployments,
replicasets, events, nodes, services, ingresses, jobs). The informer cache provides:

- O(1) reads of current state for prompt **context cards** and audit scans.
- An event stream feeding the audit engine (e.g. a pod entering `CrashLoopBackOff`
  triggers a targeted re-scan).
- A consistent `Snapshot` object passed to audit rules so a scan sees a coherent view.

In CLI mode informers are optional (lazy, on-demand list+watch for the active namespace);
in controller mode they run continuously across the fleet.

### 6.3 Read/write discipline

- All reads go through the cache or a bounded paginated list.
- All writes go through a **tool** (never directly), so they inherit policy/approval/
  dry-run/rollback/audit. The k8s layer exposes typed write helpers, but only tools call
  them.
- Server-side **dry-run** (`metav1.DryRunAll`) produces the post-change object; a
  structured diff (prior snapshot vs. dry-run result) is what the user approves.

### 6.4 Cluster context card (model-facing)

A compact, token-budgeted summary injected into the prompt so the model has grounding
without dumping raw YAML:

```
cluster: prod-eks-us-east-1  (v1.30, 42 nodes, 18 ns)
workloads: 214 deploy · 12 sts · 30 ds · 96 cronjob
health: 12 pods CrashLoopBackOff · 3 pending · 1 deploy not-progressing
top namespaces by spend: payments(31%) search(22%) media(14%)
recent events (5m): 7× FailedScheduling(search), 4× Unhealthy(api-gateway)
```

---

## 7. Memory & session architecture

Two horizons, because LLM context windows are finite and conversations + cluster state are
not.

```
   ┌────────────────────────────── SESSION ──────────────────────────────┐
   │  id, user, cluster, mode(read/propose/apply), enabled tools, budget   │
   │                                                                        │
   │  SHORT-TERM (working memory)              LONG-TERM (durable memory)    │
   │  ┌──────────────────────────┐            ┌───────────────────────────┐ │
   │  │ rolling message window    │            │ salient facts & decisions  │ │
   │  │ last N turns verbatim      │  promote   │ "api-gateway needs 90s     │ │
   │  │ + running summary of older │ ─────────▶ │  startup", embeddings for  │ │
   │  │   turns (auto-summarized)  │  recall    │  top-k semantic retrieval  │ │
   │  └──────────────────────────┘ ◀───────── └───────────────────────────┘ │
   └────────────────────────────────────────────────────────────────────────┘
```

- **Short-term.** The last *N* turns verbatim. When the window approaches the model's
  budget, older turns are compressed by a cheap model into a running summary (a
  `summarize.go` rolling reducer). Tool outputs are stored in full in the DB but only a
  trimmed/redacted rendering re-enters the prompt.
- **Long-term.** Salient, reusable facts (a recurring root cause, an approved remediation,
  a cluster quirk, a user preference) are extracted, embedded, and stored. On each turn
  the runtime recalls top-k relevant memories by vector similarity and injects them.
- **Session resume.** Everything is persisted, so `voujr --resume <id>` rehydrates
  the window + summary + memory and continues seamlessly. Cluster state is *not* memory —
  it is re-read live each turn (memory holds conclusions, not facts that may have changed).

---

## 8. Database schema

Portable across SQLite (local) and Postgres (server). `pgvector` is used for embeddings in
Postgres; SQLite stores embeddings as a blob with brute-force cosine (fine at local
scale). Full DDL lives in [`migrations/0001_init.sql`](../migrations/0001_init.sql).

```
┌────────────┐        ┌─────────────┐        ┌────────────────┐
│  users      │        │  clusters    │        │  sessions       │
├────────────┤        ├─────────────┤        ├────────────────┤
│ id (PK)     │        │ id (PK)      │        │ id (PK)         │
│ subject     │        │ name         │◀───────│ cluster_id (FK) │
│ email       │        │ context      │        │ user_id (FK)    │─┐
│ role        │        │ provider     │        │ mode            │ │
│ created_at  │        │ endpoint_hash│        │ enabled_tools   │ │
└─────┬──────┘        │ created_at   │        │ budget_cents    │ │
      │               └─────────────┘        │ created_at      │ │
      │                                       └───────┬────────┘ │
      │   ┌───────────────────────────────────────────┘          │
      │   ▼                                                       │
      │ ┌────────────────┐     ┌──────────────────────┐          │
      └▶│  messages       │     │  tool_executions      │          │
        ├────────────────┤     ├──────────────────────┤          │
        │ id (PK)         │     │ id (PK)               │          │
        │ session_id (FK) │◀──┐ │ session_id (FK)       │          │
        │ role            │   │ │ message_id (FK)       │          │
        │ content         │   └─│ tool_name             │          │
        │ tool_call_json  │     │ args_json             │          │
        │ tokens_in/out   │     │ diff_json             │          │
        │ created_at      │     │ risk                  │          │
        └────────────────┘     │ approved_by           │          │
                                │ dry_run (bool)        │          │
        ┌────────────────┐     │ status                │          │
        │  ai_usage       │     │ rollback_ref          │          │
        ├────────────────┤     │ duration_ms           │          │
        │ id (PK)         │     │ created_at            │          │
        │ session_id (FK) │     └──────────┬───────────┘          │
        │ provider/model  │                │                       │
        │ tokens_in/out   │     ┌──────────▼───────────┐          │
        │ cost_cents      │     │  audit_log (append)   │          │
        │ route_reason    │     ├──────────────────────┤          │
        │ created_at      │     │ id (PK) · hash_prev   │  tamper- │
        └────────────────┘     │ actor · action · ts   │  evident │
                                │ payload_json          │  chain   │
        ┌────────────────────┐ └──────────────────────┘          │
        │  audit_findings     │                                    │
        ├────────────────────┤   ┌────────────────────┐           │
        │ id (PK)             │   │  memories           │           │
        │ cluster_id (FK)     │   ├────────────────────┤           │
        │ category            │   │ id (PK)             │           │
        │ rule_id             │   │ session_id (FK)     │◀──────────┘
        │ severity (P0..P3)   │   │ kind                │
        │ resource_ref        │   │ text                │
        │ impact / root_cause │   │ embedding (vector)  │
        │ remediation_json    │   │ created_at          │
        │ autofixable (bool)  │   └────────────────────┘
        │ status · first/last │
        │ created_at          │   ┌────────────────────┐
        └────────────────────┘   │  approvals          │
                                  ├────────────────────┤
                                  │ id · tool_exec_id   │
                                  │ approver · decision │
                                  │ reason · ts         │
                                  └────────────────────┘
```

Key points:
- **`tool_executions`** is the operational truth: every proposed/executed action with its
  diff, approver, dry-run flag, status, and a `rollback_ref` pointing at the prior-state
  snapshot.
- **`audit_log`** is append-only and **hash-chained** (`hash_prev`) so tampering is
  detectable — this is the compliance artifact.
- **`audit_findings`** persists scan results with lifecycle (`open`/`acknowledged`/
  `resolved`) and dedup by `(cluster, rule, resource)` so findings track over time.
- **`ai_usage`** powers cost dashboards and budget enforcement.

---

## 9. Security model

Threat model in one line: *an LLM may be wrong, jailbroken, or prompt-injected via cluster
data, so no model output may cause an unapproved or out-of-scope side effect, and no
secret may reach a model or a log.*

### 9.1 Controls

| Risk | Control |
|------|---------|
| Model proposes a harmful mutation | Read-only default; mutation gate (policy + dry-run + human approval); destructive ops need extra confirmation |
| Prompt injection via logs/annotations/events | Tool outputs are treated as untrusted data, never as instructions; redaction + a "data, not commands" framing; tool allow-list can't be expanded by the model |
| Secret leakage to the model or logs | `security/redact.go` strips Secret values, tokens, kubeconfigs, and `data:` fields before anything is logged or sent to a provider |
| Over-broad cluster access | Least-privilege ServiceAccount/kubeconfig; RBAC preflight (`SelfSubjectAccessReview`) before every mutation; writes require an opt-in role |
| Stolen credentials | BYOK via OS keyring / Portkey virtual keys; provider keys never written to the DB; short-lived cloud creds via IRSA/Workload Identity |
| Repudiation | Hash-chained append-only `audit_log` records actor, args, diff, approver, result |
| Blast radius | Policy classifies scope (namespace vs. cluster-wide, replica deltas, `delete`/`drain`/`cordon`); cluster-wide or destructive actions require elevated approval |
| Supply chain | Pinned deps, SBOM, signed images (cosign), minimal distroless runtime, no shell in the image |

### 9.2 Secret handling pipeline

```
cluster/cloud  ──▶  tool result  ──▶  redact.Scrub()  ──▶  { store full (encrypted) }
                                                        └▶  { model-facing: redacted }
```

Secrets are encrypted at rest (SQLCipher / Postgres TDE or app-level envelope encryption).
The model only ever sees `kind: Secret … (12 keys, values redacted)`.

### 9.3 Trust boundaries

```
 untrusted: model output, cluster data (logs/annotations/CRDs), user free-text
 trusted:   the dispatch pipeline, policy engine, RBAC, audit log, redactor
```

Everything crossing from untrusted → trusted is validated. The model is *inside* the
untrusted zone — it's a powerful suggestion engine, not an authority.

---

## 10. RBAC strategy

Two complementary RBAC layers: the cluster's own RBAC, and the agent's internal RBAC
(server mode).

### 10.1 Kubernetes RBAC (what the agent can touch)

Ship three reference roles (in `deploy/rbac/`); operators bind what they're comfortable
with:

- **`voujr-viewer` (default).** `get/list/watch` on workloads, pods/logs, events,
  nodes, metrics, configmaps (not secret *values*), ingresses, HPAs. No write verbs. This
  is all the audit + investigation features need.
- **`voujr-operator` (opt-in).** Adds `patch/update` and `scale`/`rollout` on
  workloads in selected namespaces. No `delete` on namespaces/CRDs, no secret read.
- **`voujr-admin` (break-glass).** Adds `delete`, node `cordon/drain`, and
  cluster-scoped writes. Bound only for explicit remediation windows.

The agent **never** assumes it has a permission: before any mutation it runs a
`SelfSubjectAccessReview`; if denied, it tells the user exactly which role/verb is missing
rather than failing opaquely. In CLI mode the agent inherits the *operator's* RBAC via
their kubeconfig — it can never exceed what the human could do manually.

### 10.2 Application RBAC (server/team mode)

For shared deployments, the agent layers its own authorization on top:

| Internal role | Capabilities |
|---------------|--------------|
| `reader` | run read tools, view findings, no apply |
| `operator` | propose + apply non-destructive fixes in assigned namespaces |
| `approver` | approve others' proposed mutations |
| `admin` | manage clusters, roles, budgets, destructive actions |

Approver ≠ proposer can be enforced (separation of duties): the user who proposes a P0 fix
isn't allowed to approve their own action. SSO (OIDC) maps IdP groups → internal roles.

---

## 11. Multi-cluster support design

```
                 ┌──────────────────────────────────────────────┐
                 │            Cluster Registry                    │
                 │            internal/k8s/multicluster.go        │
                 │  name → { kubeconfig ctx | in-cluster SA |     │
                 │           OIDC/EKS/GKE/AKS auth }, clientset,  │
                 │           dynamic, discovery, informer cache,  │
                 │           RBAC profile, health                 │
                 └───────────────┬───────────────┬───────────────┘
            ┌────────────────────┘               └────────────────────┐
            ▼                                                          ▼
   ┌──────────────┐      ┌──────────────┐                  ┌──────────────┐
   │ prod-eks      │      │ stage-gke     │        …         │ dr-aks        │
   │ viewer+oper   │      │ viewer        │                  │ viewer        │
   └──────────────┘      └──────────────┘                  └──────────────┘
```

- **Registry.** Each cluster is registered with its auth method, lazily-built clients, an
  optional informer cache, its RBAC profile, and a health probe. The active cluster is a
  session property; the agent can switch (`/cluster prod`) or operate across several.
- **Auth.** Supports kubeconfig contexts, in-cluster ServiceAccount, and cloud-native
  auth (EKS IAM/`aws eks get-token`, GKE Workload Identity, AKS AAD) via exec credential
  plugins.
- **Cross-cluster queries.** Read tools accept a `clusters: [...]` selector; the audit
  engine can fan out a scan across the fleet concurrently and aggregate findings, with
  per-cluster RBAC respected independently.
- **Isolation.** A failure, throttle, or breach in one cluster's client is contained —
  each handle has its own rate limiter, circuit breaker, and credentials. No shared
  mutable state across clusters.
- **Context safety.** Mutations always name their target cluster explicitly in the
  approval prompt; "wrong-cluster" mistakes are prevented by requiring the active cluster
  to match the tool's `cluster` arg.

---

## 12. Scalability considerations

**CLI mode** scales trivially (one user, one process). The interesting scaling is the
in-cluster controller and large/many clusters.

| Dimension | Strategy |
|-----------|----------|
| **Large clusters (10k+ pods)** | Informer caches + field/label selectors; paginated lists; audit rules operate on the cached snapshot, not live API calls; per-resource-kind concurrency caps |
| **Many clusters (fleet)** | Shard clusters across controller replicas via consistent hashing + leader election (one owner per cluster); registry is the shard map |
| **API-server pressure** | Client-side rate limiting (`flowcontrol`), respect APF, exponential backoff, prefer watch over poll, coalesce re-scans triggered by event storms |
| **LLM cost & latency** | Model router downgrades trivial turns; prompt-prefix caching; summarize long histories; batch audit explanations; cap tokens per turn |
| **Concurrent sessions** | Stateless request handling where possible; session state in Postgres; horizontal replicas behind the session manager; sticky routing by session id |
| **Audit throughput** | Rules are pure functions over a snapshot → embarrassingly parallel (`errgroup` with a worker pool); incremental re-scan of only changed resources |
| **Storage growth** | Partition `audit_log`/`messages`/`ai_usage` by time; retention + archival to object storage; findings deduped, not duplicated per scan |
| **Backpressure** | Bounded queues between event stream → audit → alerting; shed/coalesce under load rather than unbounded fan-out |

Targets: interactive read queries < 1.5s p95 to first token; audit scan of a 5k-pod
cluster < 30s; controller memory roughly linear in cached object count (~few hundred MB
per 10k pods with selective informers).

---

## 13. Example Go implementations

Representative, idiomatic implementations live in `internal/`. The most instructive
starting points:

| Concept | File |
|---------|------|
| Tool contract + result model | [`internal/tools/tool.go`](../internal/tools/tool.go) |
| Registry, schema gen, dispatch chain | [`internal/tools/registry.go`](../internal/tools/registry.go) |
| Approval / dry-run / rollback middleware | [`internal/tools/approval.go`](../internal/tools/approval.go) |
| A concrete read+write tool | [`internal/tools/kubectl.go`](../internal/tools/kubectl.go) |
| Provider interface + types | [`internal/ai/provider.go`](../internal/ai/provider.go) |
| Portkey gateway adapter | [`internal/ai/portkey.go`](../internal/ai/portkey.go) |
| Model router | [`internal/ai/router.go`](../internal/ai/router.go) |
| Agent runtime loop | [`internal/agent/loop.go`](../internal/agent/loop.go) |
| Multi-cluster registry | [`internal/k8s/multicluster.go`](../internal/k8s/multicluster.go) |
| Audit engine + rule interface | [`internal/audit/engine.go`](../internal/audit/engine.go) |
| Example reliability rule | [`internal/audit/rules/probes.go`](../internal/audit/rules/probes.go) |
| Session/memory store | [`internal/session/memory.go`](../internal/session/memory.go) |
| Bubble Tea chat model | [`internal/tui/app.go`](../internal/tui/app.go) |
| Prometheus metrics | [`internal/observability/metrics.go`](../internal/observability/metrics.go) |
| Wiring | [`cmd/voujr/main.go`](../cmd/voujr/main.go) |

These are written to read as a coherent system rather than to compile against pinned
upstream APIs verbatim — run `go mod tidy` and adjust client-go/bubbletea minor APIs as
needed.

---

## 14. Deployment strategy

### 14.1 Local CLI

```bash
go install github.com/voujr/voujr/cmd/voujr@latest   # or download a release
export ANTHROPIC_API_KEY=…        # or PORTKEY_API_KEY for the gateway
voujr --context prod-eks      # read-only by default
```

Distribution: GoReleaser cross-compiles static binaries (linux/darwin/windows × amd64/
arm64), Homebrew tap, `.deb`/`.rpm`, and a Krew plugin so it's reachable as
`kubectl agent`.

### 14.2 In-cluster controller (team mode)

- **Image.** Distroless, non-root, read-only root FS, no shell. Multi-stage build in
  [`deploy/Dockerfile`](../deploy/Dockerfile). Signed with cosign; SBOM attached.
- **Helm chart** (`deploy/helm/`) provisions: Deployment (2+ replicas, leader election),
  the scoped ServiceAccount + reference RBAC, a Postgres connection (external or
  operator-managed), Secrets via External Secrets Operator/Vault, a ServiceMonitor for
  Prometheus, NetworkPolicies (egress only to API servers + provider endpoints), and a
  PodSecurityContext meeting the `restricted` PSS.
- **Config** via a ConfigMap + Secret; provider keys via virtual keys / ESO, never inline.
- **Rollout.** Standard rolling update; the controller is stateless aside from leader
  election, so drains cleanly.

### 14.3 CI/CD

`build → vet → test -race → golangci-lint → govulncheck → docker build → cosign sign →
helm lint → publish`. Promotion via GitOps (Argo CD) into stage then prod; the agent's own
`argocd_app_status` tool can verify its own rollout.

### 14.4 Configuration precedence

`flags > env > project config (./voujr.yaml) > user config (~/.voujr/config.yaml)
> defaults`. Validated at startup (`config.Validate()`); the process refuses to start in
`apply` mode without an audit sink configured.

---

## 15. Trade-off analysis

### 15.1 vs. `kubectl`

| | `kubectl` | voujr |
|--|-----------|------------|
| Interface | exact, imperative, you must know the verb/resource | natural language; discovers intent |
| Correlation | you join data in your head | correlates pods↔events↔metrics↔logs↔config automatically |
| Safety | does exactly what you type (incl. mistakes) | dry-run + diff + approval + rollback + audit by default |
| Determinism | fully deterministic | model adds nondeterminism (mitigated: tools are deterministic, model only chooses them) |
| Speed for experts | unbeatable for known one-liners | slower for a single known command; far faster for open-ended investigation |
| Auditability | shell history (lossy) | structured, hash-chained audit of args+diff+approver |
| Learning curve | steep | low to start, depth on demand |

**Verdict.** voujr doesn't replace `kubectl`; it *wraps and supervises* it. Experts
keep `kubectl` for surgical commands; voujr wins for investigation, audit,
multi-step remediation, and giving less-expert operators a safe on-ramp. The agent emits
the exact `kubectl` it would run, so it's also a teaching tool.

### 15.2 vs. Claude Code

Claude Code is a general terminal coding agent; voujr borrows its UX and loop shape
but specializes the substrate.

| | Claude Code | voujr |
|--|-------------|------------|
| Domain | source code & local dev | live clusters & cloud infra |
| Primary side effect | edit files, run shell | mutate running production systems |
| Safety posture | edit/permission prompts; reversible via VCS | read-only default + approval + dry-run + rollback (mutations are higher-stakes and not always VCS-reversible) |
| State model | the filesystem | live cluster state (changes under you) + informer cache |
| Tools | file edit, bash, search | kubectl/helm/prom/loki/argocd/cloud, schema-typed and policy-gated |
| Providers | Anthropic | multi-provider via Portkey (OpenAI/Anthropic/Gemini) + routing/failover |
| Memory | repo + session | session + long-term operational memory (root causes, cluster quirks) |
| Continuous mode | interactive | also a controller doing 24/7 audit + alerting |

**Verdict.** The hard differences come from the domain: cluster state is *live and shared*
(so memory holds conclusions, not snapshots, and every read is fresh), mutations are
*high-blast-radius and not trivially revertible* (so the safety chain is heavier than
file edits), and operations are *fleet-scale and multi-tenant* (so RBAC, multi-cluster,
and a server mode are first-class). The multi-provider gateway is a deliberate divergence
for enterprise BYOK/procurement reality.

### 15.3 Key internal trade-offs

- **Gateway (Portkey) vs. direct SDKs.** Gateway buys multi-provider, failover, BYOK, and
  unified cost/observability for an added network hop and dependency. We keep a direct
  adapter as break-glass. *Chosen: gateway default.*
- **Informer cache vs. live reads.** Cache gives latency + low API pressure at the cost of
  staleness and memory. *Chosen: cache for audit/context cards; live reads for anything a
  mutation depends on.*
- **Model autonomy vs. approval friction.** Full autonomy is faster but unsafe on prod.
  *Chosen: read-only autonomy, write-by-approval, with per-policy auto-approval for
  low-risk classes the operator opts into.*
- **SQLite vs. Postgres.** Local simplicity vs. team scale. *Chosen: both behind one
  `Store` interface; SQLite for CLI, Postgres for controller.*
- **Heuristic vs. LLM routing classifier.** Heuristics are free and predictable but
  coarse. *Chosen: heuristic first, optional tiny-model refinement when ambiguous.*
```
