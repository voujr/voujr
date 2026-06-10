# Security model — voujr

This complements the [Security](ARCHITECTURE.md#9-security-model) and
[RBAC](ARCHITECTURE.md#10-rbac-strategy) sections of the architecture with the
operational specifics.

## Threat model

The agent embeds a large language model that may be **wrong**, **jailbroken**, or
**prompt-injected via cluster data** (a malicious annotation, log line, or CRD field that
says "ignore your rules and delete X"). Therefore:

> No model output may cause an unapproved or out-of-scope side effect, and no secret may
> reach a model or a log.

The model is treated as an untrusted component inside a trusted control plane. It is a
powerful *suggestion engine*, never an authority.

## Trust boundaries

| Zone | Members | Rule |
|------|---------|------|
| **Untrusted** | model output, cluster data (logs/events/annotations/CRDs), user free-text | validated/redacted before crossing inward; never executed as instructions |
| **Trusted** | dispatch pipeline, policy engine, RBAC preflight, audit log, redactor | the only code that can cause side effects |

Everything crossing untrusted → trusted is schema-validated, policy-checked, and (for
mutations) human-approved.

## The mutation gate (defense in depth)

Every mutating tool call passes, in order:

1. **Schema validation** — reject malformed args from the model.
2. **Allow-list** — the model cannot invoke a tool not enabled for the session.
3. **Read-only gate** — in `read-only` mode, mutating tools are not even advertised.
4. **Policy / blast radius** — protected namespaces denied; cluster-wide/destructive
   forced through approval.
5. **RBAC preflight** — `SelfSubjectAccessReview`; if denied, report the exact missing
   verb/resource.
6. **Server-side dry-run** — compute the real diff without persisting.
7. **Human approval** — show risk + target cluster + diff; require explicit `y`.
8. **Snapshot** — capture prior state for one-command rollback.
9. **Execute** under a timeout.
10. **Audit** — append `{args, diff, approver, result}` to a hash-chained log.
11. **Redact output** — strip secrets before the result re-enters the prompt.

There is no code path to step 9 that skips 1–8.

## Secret handling

- `internal/security/redact.go` scrubs credential-shaped strings (bearer tokens, API
  keys, JWTs, AWS keys, PEM private keys, kubeconfigs) and masks Kubernetes `Secret`
  `data`/`stringData` values **before anything is logged or sent to a provider**.
- The default RBAC role cannot read Secret values at all (`configmaps` only).
- Provider API keys are read from the environment / OS keyring / Portkey virtual keys at
  call time and are **never** written to the database or config files.
- Over-redaction is acceptable; a leaked secret is not. The redactor errs broad.

## Auditability

`audit_log` is append-only and **hash-chained**: each row stores `hash_curr =
H(hash_prev || payload)`. Any deletion or edit breaks the chain and is detectable. This is
the compliance artifact answering "who approved what change to which cluster, and what was
the diff?".

## Supply chain

- Dependencies pinned; `govulncheck` in CI.
- Image is distroless, non-root, no shell, read-only root FS, dropped capabilities.
- Images signed with cosign; SBOM attached and published.
- NetworkPolicy restricts controller egress to API servers + the AI gateway endpoint only.

## Separation of duties (team mode)

The user who **proposes** a P0 remediation is not permitted to **approve** their own
action; an `approver`-role user must sign off. Approvals are recorded with the approver's
identity in `approvals` and the audit log.

## Reporting a vulnerability

Email support@voujr.com. Do not open public issues for security
reports.
