# From a service manager to Kubernetes

How the functions of a classic on-premise service manager (init scripts plus an upgrade runbook on
customer machines) map to Kubernetes and to this operator. "Kubernetes" means the platform already does
it; "operator" means this code does it, because Kubernetes alone does not.

| Service-manager function | Where it goes | Notes |
|---|---|---|
| Start/stop services in order | Kubernetes + operator | StatefulSets for PostgreSQL and RabbitMQ, Deployments for services; the operator orders the rollout (broker, core, gateway) and waits for readiness |
| Restart a crashed process | Kubernetes | liveness probes and the kubelet |
| Health check before declaring "up" | Kubernetes + operator | readiness probes; the operator's Verify step requires every component ready |
| Configuration files per site | operator | `spec.config`, migrated per release (renames, removals, defaults) and validated before use; one ConfigMap per release so a rollback uses the old config |
| Upgrade runbook | operator | `internal/plan`: path through supported releases, one verified hop at a time |
| Pre-upgrade checks | operator | preflight: path, config, health, backup space, queues drained; blocks before any change |
| Database backup before upgrade | operator | Backup Job to a dedicated volume before irreversible migrations |
| Schema migration | operator | Migrate Job with `backoffLimit: 0`; never retried blindly |
| Rollback | operator | reverse migration, or restore from backup, then old services; never past the last verified release |
| Log collection for support | stackctl | `support-bundle` with redaction of credentials and subscriber identifiers |
| Credentials | operator | random Secret created once, never logged or overwritten |
| Host firewall, users, sudo rules | OpenShift | SCCs, NetworkPolicy, RBAC; the operator needs no host access |
| Scheduled maintenance windows | not covered | upgrades start when the spec changes; a window would be a field on the spec plus a check in `startUpgrade` |
| Offline installation | partly | images from a configurable registry (`STACK_IMAGE_REGISTRY`); mirroring is the customer's registry tooling |

## What stays hard

- **Stateful upgrades.** Kubernetes rolls stateless services well; the database and broker need the
  ordering and safeguards above.
- **Sites that drift.** Hand-edited config and half-finished earlier upgrades are the main source of
  failures. Preflight and config migration are where most of the lab's prevented incidents come from.
