# ADR 0001: an operator, not Helm hooks, runs upgrades

Status: accepted

## Context

The platform runs at customer sites, often air-gapped, with a database whose large migrations cannot
be reversed. Today an engineer follows a runbook. The obvious Kubernetes move is a Helm chart with the
migration as a pre-upgrade hook and `--atomic`.

## Decision

A controller-runtime operator owns the lifecycle. Helm (or plain manifests) only installs the operator
and the CRD.

## Why

- `--atomic` reverts Kubernetes objects, not the database. After a failed irreversible migration it
  puts the old application on the new schema. The lab counts this as a broken site.
- An upgrade across several releases has to stop at every release and verify it; Helm upgrades in
  one jump.
- Preflight needs live facts (queue depth, free volume space, component health) and has to block
  before anything changes. A hook runs after Helm has decided to proceed.
- Status conditions and history on the custom resource are what a support engineer and a customer's
  monitoring read; Helm release history does not carry the reason.

## Consequences

More code to own (controller, CRD, RBAC). Mitigated by keeping decisions in pure functions
(`internal/plan`) tested without a cluster, and envtest tests for the controller.
