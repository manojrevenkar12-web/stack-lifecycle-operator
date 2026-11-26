# ADR 0002: one step per reconcile, plan stored in status

Status: accepted

## Decision

When an upgrade starts, the full plan (steps, per-release config, preflight result) is written to
`.status`. Each reconcile advances at most one step and records its state. A failed step appends the
rollback steps to the same list.

## Why

- The operator can restart at any moment (leader change, node drain) and continue from status.
- A person reading `kubectl get pstack -o yaml` sees exactly what ran, what is next and why it stopped.
- Jobs have deterministic names (`<stack>-migrate-20`) and are created only if missing, so a repeated
  reconcile never runs a migration twice.

## Consequences

Status grows during an upgrade (bounded: one entry per step, history capped at 20 entries).
