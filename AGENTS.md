# AGENTS.md

Rules for anyone changing this repository, people and coding agents alike.

## Invariants

1. **The plan decides, the controller executes.** Upgrade order, preflight and rollback live in
   `internal/plan` as pure functions over the release catalog. The controller only applies one step per
   reconcile and records it in status. The lab calls the same functions, so a change to the plan
   changes the lab numbers.
2. **Nothing changes on a failed preflight.** `startUpgrade` returns before touching any object when a
   check fails.
3. **Back up before a migration that cannot be reversed.** `plan.Build` adds Backup and Quiesce for
   every irreversible hop unless the policy says otherwise. Turning that off is an explicit,
   per-stack choice (`spec.upgrade.skipBackup`), never a default.
4. **Roll back to the last verified release, never further.** A rolled-back target is not retried in
   the same generation; a person changes the spec to try again.
5. **The broker is never downgraded.** Newer brokers serve older clients; a downgrade would drop its
   queues.
6. **Restricted-v2 by construction.** No fixed UID, no capabilities, read-only root, RuntimeDefault
   seccomp, no service-account token in workloads. `internal/render` tests and
   `stackctl openshift-lint` enforce it.
7. **Least privilege.** `config/rbac/role.yaml` grants exactly what the `+kubebuilder:rbac` markers ask
   for (`rbac_test.go`). Secrets: get and create only, read uncached.
8. **Credentials are generated once, never logged, never overwritten.**
9. **Reports are generated.** `README.md` (between the results markers), `reports/RESULTS.md` and
   `dashboard/data/results.json` come from `go run ./cmd/lab report`. CI fails if they are stale.
10. **The held-out split runs once.** `results/HELDOUT.lock` exists after it has run; `lab sim` refuses
    to run it again, and `report` refuses if `config/lab.yaml` changed afterwards.

## Commands

```bash
make test        # go vet, envtest controller tests, unit tests, Python tests and ruff
make lint        # render manifests and lint them with stackctl openshift-lint
make lab-dev     # rerun the dev split and regenerate the reports
make check       # CI's staleness check
```

## Conventions

- Go: standard library style, `gofmt`, errors wrapped with context, no panics outside `main`.
- Every behaviour change to `internal/plan` or `internal/controller` comes with a test.
- Commit trailers: `AI-Assisted: yes` (or a `Co-Authored-By` naming the assistant) when an assistant
  wrote part of the change; `Reviewed-by: Name <email>` once a person has reviewed it. The review gate
  in CI reads them (`config/review-gate.yaml`).
- Settings live in config files (`config/lab.yaml`, `internal/release/catalog.json`), not in code.
