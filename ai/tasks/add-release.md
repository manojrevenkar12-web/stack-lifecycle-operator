# Task: add release <VERSION>

Context: read AGENTS.md first. The release catalog is the single source of truth for upgrade paths,
schema versions and config changes.

1. Add the release to `internal/release/catalog.json` with `upgradeFrom`, `schema`, `messaging`,
   `reversible` and its config rules (known, required, renames, removed, defaults, values).
2. Add a case to `internal/release/release_test.go` for the new path and to
   `internal/plan/plan_test.go` for the plan and the rollback of the new hop.
3. Run `make test`. Do not edit generated reports by hand; if the target changes, update
   `config/lab.yaml` and run `make lab-dev`.

Out of scope: changing the controller. If the new release needs a new step kind, stop and write an ADR first.

Commit with `AI-Assisted: yes`. A person adds `Reviewed-by:` after review.
