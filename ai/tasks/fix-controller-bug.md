# Task: fix a controller bug

1. Reproduce it as an envtest case in `internal/controller/platformstack_controller_test.go`. The
   harness plays the cluster: `h.failJob`, `h.hang` and `h.facts` set up failures, queue depth and disk.
2. Fix it in `internal/controller` or, if it is a planning error, in `internal/plan` (with a unit test there).
3. `make test` and `make lint` must pass. Keep RBAC unchanged unless the fix needs a new verb; then
   update the marker and `config/rbac/role.yaml` together.

The review gate treats `internal/controller` as security-sensitive: an AI-assisted change needs a test
change and a named reviewer.
