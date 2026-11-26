# SKILLS.md

Repeatable tasks in this repository. Each one lists the files it touches and the check that proves it is done.

| Task | Files | Done when |
|---|---|---|
| Add a release | `internal/release/catalog.json`, `internal/release/release_test.go` | `go test ./internal/release ./internal/plan` passes; `lab sim --split dev` and `lab report` rerun |
| Add a preflight check | `internal/plan/plan.go`, `internal/plan/plan_test.go`, `internal/lab/lab.go` | a test shows the check blocking and the lab counts its blocks |
| Change what the operator renders | `internal/render`, `config/rbac/role.yaml` if verbs change | `make test` and `make lint` pass with 0 errors |
| Change a lab assumption | `config/lab.yaml` | dev split rerun, reports regenerated; never after the held-out lock without saying so in the README |
| Prepare a support bundle at a site | `tools/stackctl` | `stackctl support-bundle <dir>` output lists every redaction in `REDACTIONS.json` |
| Review an AI-assisted change | `config/review-gate.yaml` | `stackctl review-gate --base origin/main` passes |

Templates for handing a task to a coding agent: `ai/tasks/`.
