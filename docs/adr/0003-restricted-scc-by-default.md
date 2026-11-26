# ADR 0003: OpenShift restricted-v2 by default

Status: accepted

## Decision

Every pod the operator creates meets OpenShift's `restricted-v2` SCC and the Kubernetes "restricted"
Pod Security Standard without extra grants: no fixed UID or fsGroup, `runAsNonRoot`, RuntimeDefault
seccomp, all capabilities dropped, no privilege escalation, read-only root with an emptyDir for /tmp,
no service-account token.

## Why

Customer clusters at regulated operators rarely grant `anyuid` or privileged SCCs to vendor software,
and asking for them delays every installation by weeks of security review.

## Consequences

Images must run as an arbitrary UID with group 0 write access where they write at all. PostgreSQL and
RabbitMQ images must be the OpenShift-compatible variants. Checked by `internal/render` tests and
`stackctl openshift-lint` in CI.
