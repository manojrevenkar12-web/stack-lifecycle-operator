"""Lint rendered manifests against OpenShift's restricted-v2 SCC and least-privilege RBAC.

The operator renders its objects with `go run ./cmd/render`; this checks them without a cluster,
so a change that would be refused by the restricted SCC (or by Pod Security "restricted") fails in
CI instead of at a customer's site.
"""

from __future__ import annotations

from collections.abc import Iterable, Iterator
from dataclasses import dataclass

import yaml

POD_OWNERS = {"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job"}


@dataclass(frozen=True)
class Finding:
    severity: str  # "error" or "warning"
    obj: str  # Kind/name
    rule: str
    message: str

    def __str__(self) -> str:
        return f"{self.severity:7} {self.obj:40} {self.rule:28} {self.message}"


def load_documents(text: str) -> list[dict]:
    return [d for d in yaml.safe_load_all(text) if isinstance(d, dict)]


def pod_spec(doc: dict) -> dict | None:
    kind = doc.get("kind")
    if kind == "Pod":
        return doc.get("spec") or {}
    if kind == "CronJob":
        return (((doc.get("spec") or {}).get("jobTemplate") or {}).get("spec") or {}).get("template", {}).get("spec")
    if kind in POD_OWNERS:
        return ((doc.get("spec") or {}).get("template") or {}).get("spec")
    return None


def _name(doc: dict) -> str:
    return f"{doc.get('kind')}/{(doc.get('metadata') or {}).get('name', '?')}"


def lint_pod(name: str, spec: dict) -> Iterator[Finding]:
    def err(rule: str, msg: str) -> Finding:
        return Finding("error", name, rule, msg)

    def warn(rule: str, msg: str) -> Finding:
        return Finding("warning", name, rule, msg)

    psc = spec.get("securityContext") or {}
    if psc.get("runAsNonRoot") is not True:
        yield err("run-as-non-root", "pod securityContext.runAsNonRoot must be true")
    if "runAsUser" in psc or "fsGroup" in psc:
        yield err("no-fixed-uid", "do not fix runAsUser/fsGroup; restricted-v2 assigns an id from the namespace range")
    if (psc.get("seccompProfile") or {}).get("type") != "RuntimeDefault":
        yield err("seccomp", "seccompProfile.type must be RuntimeDefault")
    for field in ("hostNetwork", "hostPID", "hostIPC"):
        if spec.get(field):
            yield err("host-namespaces", f"{field} is not allowed")
    own_account = spec.get("serviceAccountName") not in (None, "", "default")
    if spec.get("automountServiceAccountToken") is not False and not own_account:
        yield warn("service-account-token", "workloads that do not call the API should not mount a token")
    for vol in spec.get("volumes") or []:
        if "hostPath" in vol:
            yield err("host-path", f"volume {vol.get('name')} uses hostPath")
    for c in (spec.get("initContainers") or []) + (spec.get("containers") or []):
        cname = f"{name}/{c.get('name')}"
        sc = c.get("securityContext") or {}
        if sc.get("privileged"):
            yield Finding("error", cname, "privileged", "privileged containers are not allowed")
        if sc.get("allowPrivilegeEscalation") is not False:
            yield Finding("error", cname, "privilege-escalation", "allowPrivilegeEscalation must be false")
        caps = sc.get("capabilities") or {}
        if [x.upper() for x in caps.get("drop") or []] != ["ALL"]:
            yield Finding("error", cname, "capabilities", "capabilities.drop must be [ALL]")
        if caps.get("add"):
            yield Finding("error", cname, "capabilities", f"adds capabilities {caps['add']}")
        if "runAsUser" in sc:
            yield Finding("error", cname, "no-fixed-uid", "container fixes runAsUser")
        if sc.get("readOnlyRootFilesystem") is not True:
            yield Finding("warning", cname, "read-only-root", "root filesystem should be read-only")
        image = c.get("image", "")
        tag = image.rsplit("/", 1)[-1]
        if ":" not in tag and "@" not in image:
            yield Finding("error", cname, "image-pinned", f"image {image!r} has no tag")
        elif tag.endswith(":latest"):
            yield Finding("error", cname, "image-pinned", f"image {image!r} uses :latest")
        res = c.get("resources") or {}
        if not (res.get("limits") or {}).get("memory"):
            yield Finding("error", cname, "resources", "memory limit missing")
        if not (res.get("requests") or {}).get("cpu"):
            yield Finding("warning", cname, "resources", "cpu request missing")
        for p in c.get("ports") or []:
            if p.get("hostPort"):
                yield Finding("error", cname, "host-port", "hostPort is not allowed")
            if 0 < int(p.get("containerPort", 1024)) < 1024:
                yield Finding("error", cname, "privileged-port", "non-root containers cannot bind below 1024")


def lint_rbac(doc: dict) -> Iterator[Finding]:
    name = _name(doc)
    for rule in doc.get("rules") or []:
        verbs, resources = rule.get("verbs") or [], rule.get("resources") or []
        if "*" in verbs or "*" in resources or "*" in (rule.get("apiGroups") or []):
            yield Finding("error", name, "rbac-wildcard", f"wildcard in {rule}")
        if "secrets" in resources and {"list", "watch"} & set(verbs):
            yield Finding("error", name, "rbac-secrets", "list/watch on secrets reads every secret in scope")
        if {"escalate", "bind", "impersonate"} & set(verbs):
            yield Finding("error", name, "rbac-escalation", f"verbs {verbs} allow privilege escalation")
        if "pods/exec" in resources:
            yield Finding("warning", name, "rbac-exec", "pods/exec gives shell access to workloads")


def lint(docs: Iterable[dict]) -> list[Finding]:
    out: list[Finding] = []
    for doc in docs:
        spec = pod_spec(doc)
        if spec is not None:
            out.extend(lint_pod(_name(doc), spec))
        if doc.get("kind") in {"Role", "ClusterRole"}:
            out.extend(lint_rbac(doc))
    return out
