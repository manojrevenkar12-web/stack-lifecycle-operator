from pathlib import Path

from stackctl import lint

FIXTURES = Path(__file__).parent / "fixtures"


def test_rendered_operator_manifests_are_restricted_clean():
    findings = lint.lint(lint.load_documents((FIXTURES / "rendered.yaml").read_text()))
    assert findings == [], "\n".join(map(str, findings))


BAD = """
apiVersion: apps/v1
kind: Deployment
metadata: {name: legacy}
spec:
  template:
    spec:
      hostNetwork: true
      securityContext: {runAsUser: 0}
      volumes: [{name: logs, hostPath: {path: /var/log}}]
      containers:
        - name: app
          image: registry/app:latest
          ports: [{containerPort: 80}]
          securityContext: {privileged: true, capabilities: {add: [NET_ADMIN]}}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata: {name: too-much}
rules:
  - {apiGroups: [""], resources: [secrets], verbs: [get, list, watch]}
  - {apiGroups: ["*"], resources: ["*"], verbs: ["*"]}
"""


def test_legacy_manifest_breaks_every_rule():
    rules = {f.rule for f in lint.lint(lint.load_documents(BAD)) if f.severity == "error"}
    assert {
        "run-as-non-root",
        "no-fixed-uid",
        "seccomp",
        "host-namespaces",
        "host-path",
        "privileged",
        "privilege-escalation",
        "capabilities",
        "image-pinned",
        "resources",
        "privileged-port",
        "rbac-secrets",
        "rbac-wildcard",
    } <= rules


def test_cronjob_and_untagged_image():
    doc = {
        "kind": "CronJob",
        "metadata": {"name": "nightly"},
        "spec": {"jobTemplate": {"spec": {"template": {"spec": {"containers": [{"name": "c", "image": "busybox"}]}}}}},
    }
    rules = {f.rule for f in lint.lint([doc])}
    assert "image-pinned" in rules and "run-as-non-root" in rules
