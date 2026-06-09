# voujr

> A terminal-native, AI-powered Kubernetes copilot — like Claude Code, specialized for cluster operations, auditing, troubleshooting, optimization, and remediation.

`voujr` lets operators talk to their clusters in natural language. It understands
live cluster state, reasons about workload relationships, runs an audit engine across
reliability/security/cost/optimization, proposes step-by-step remediation, and — only
with explicit approval — executes changes against the cluster.

```
$ voujr

> Why are pods in production restarting?

Analyzing cluster…
  ✓ Connected to production (context: prod-eks-us-east-1)
  ✓ Scanned 312 pods across 18 namespaces
  ✓ Found 12 restarting pods in deployment/api-gateway

Root cause
  api-gateway readiness probe fails during JVM warm-up (avg 47s) but the
  startupProbe budget is 30s, so the kubelet kills the container before it is ready.

Recommendation  (P1 · reliability)
  Raise startupProbe failureThreshold 6 → 18 (30s → 90s budget).

  ▸ kubectl -n prod patch deploy/api-gateway --type=json \
      -p '[{"op":"replace","path":"/spec/.../startupProbe/failureThreshold","value":18}]'

Apply this fix? [y/N/dry-run]
```

## Status

This repository is an **architecture + reference scaffold**. The design is documented
in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md); the `internal/` packages contain
representative, idiomatic implementations of each subsystem that compile into a coherent
skeleton. It is a foundation to build a production system on, not a finished product.

Verified with Go 1.26.4 on Windows: `go build ./...`, `go vet ./...`, and `gofmt`
are all clean, the binary builds and its CLI runs, and unit tests cover the
safety-critical paths (the read-only mutation gate, model routing, and secret
redaction) — `go test ./...` passes.

## Quick start

```bash
make tidy        # resolve dependencies (needs Go 1.23+)
make build       # -> bin/voujr
export ANTHROPIC_API_KEY=...      # or OPENAI_API_KEY / GEMINI_API_KEY / PORTKEY_API_KEY
./bin/voujr --context prod-eks-us-east-1
```

By default the agent runs **read-only**. Any mutating action is gated behind an
approval prompt, supports `--dry-run`, and is recorded in an immutable audit log.

## Architecture at a glance

```
                         ┌──────────────────────────┐
   terminal  ◀──tokens──▶│   TUI  (Bubble Tea)      │
                         └────────────┬─────────────┘
                                      │ events
                         ┌────────────▼─────────────┐
                         │     Agent Runtime         │  ReAct loop:
                         │  plan → act → observe     │  reason, call tools,
                         └───┬───────────┬───────┬───┘  observe, repeat
              ┌──────────────┘           │       └──────────────┐
   ┌──────────▼─────────┐   ┌────────────▼──────┐   ┌───────────▼─────────┐
   │  AI Orchestration  │   │   Tool Registry    │   │  Session / Memory   │
   │ Portkey · routing  │   │  (MCP-style)       │   │  SQLite/Postgres    │
   │ failover · BYOK    │   │ approval · dry-run │   │  short+long term    │
   └──────────┬─────────┘   └────────┬───────────┘   └─────────────────────┘
              │                       │
   OpenAI/Anthropic/Gemini   kubectl·helm·prom·argocd·cloud
                                      │
                         ┌────────────▼─────────────┐
                         │  Kubernetes Integration   │  client-go, informers,
                         │  multi-cluster registry   │  read-by-default
                         └────────────┬─────────────┘
                                      │
                         ┌────────────▼─────────────┐
                         │      Audit Engine         │  reliability · security
                         │  rules → findings → fixes │  cost · optimization
                         └───────────────────────────┘
```

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the full design including the
agent runtime, AI routing, MCP tool model, DB schema, security/RBAC model,
multi-cluster strategy, scalability, deployment, and trade-offs vs. Claude Code & kubectl.

## Repository layout

| Path | What lives here |
|------|-----------------|
| `cmd/voujr` | CLI entrypoint and dependency wiring |
| `internal/agent` | Agent runtime — the reason/act/observe loop |
| `internal/ai` | Provider abstraction, Portkey gateway, model router, streaming |
| `internal/tools` | MCP-style tool registry, approval/dry-run middleware, concrete tools |
| `internal/k8s` | client-go integration, multi-cluster registry, live state cache |
| `internal/audit` | Audit engine, rule interface, findings model, example rules |
| `internal/session` | Conversation + memory persistence |
| `internal/store` | Database access layer |
| `internal/tui` | Bubble Tea terminal UI |
| `internal/observability` | Prometheus metrics, OpenTelemetry tracing |
| `internal/incident` | Slack / PagerDuty integrations |
| `internal/config` | Configuration loading & validation |
| `migrations` | SQL schema migrations |
| `deploy` | Dockerfile, Helm chart, RBAC manifests |

## Safety model (TL;DR)

- **Read-only by default.** Mutations require an explicit approval gate.
- **Least privilege.** The agent's ServiceAccount/kubeconfig grants only what its
  enabled tools need; writes need a separate, opt-in role.
- **Dry-run first.** Mutating tools run `--dry-run=server` and show a diff before apply.
- **Rollback.** Every applied change snapshots the prior object for one-command revert.
- **Immutable audit.** Every tool call (args, diff, approver, result) is appended to an
  audit log; secrets are redacted before they ever reach a model or a log.

## License

Apache-2.0 (placeholder).
