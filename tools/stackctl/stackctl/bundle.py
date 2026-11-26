"""Support bundles that are safe to send to the vendor.

A support bundle collects what an engineer needs to diagnose a failed upgrade at a customer site:
the PlatformStack status, events, pod and job descriptions and logs. Lawful-interception and
data-retention platforms hold some of the most sensitive data there is, so the bundle redacts
credentials and personal identifiers before anything leaves the site, and lists every redaction
so the customer can check it.
"""

from __future__ import annotations

import io
import json
import re
import tarfile
from dataclasses import dataclass, field
from pathlib import Path

import yaml

REDACTED = "[REDACTED]"

PATTERNS: list[tuple[str, re.Pattern[str]]] = [
    ("pem", re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----", re.S)),
    ("bearer", re.compile(r"(?i)\bbearer\s+[a-z0-9._~+/-]+=*")),
    ("url-credentials", re.compile(r"(?i)\b([a-z][a-z0-9+.-]*://[^:/\s@]+):[^@\s/]+@")),
    (
        "key-value",
        re.compile(r"(?i)\b([a-z0-9_.-]*(password|passwd|secret|token|apikey|api_key)[a-z0-9_.-]*)\s*[=:]\s*(\S+)"),
    ),
    ("msisdn", re.compile(r"(?<![\d.])\+?\d{1,3}[ -]?\d{3}[ -]?\d{6,8}(?![\d.])")),
    ("imsi", re.compile(r"\b\d{15}\b")),
    ("email", re.compile(r"\b[\w.+-]+@[\w-]+\.[\w.-]+\b")),
]

SENSITIVE_ENV = re.compile(r"(?i)(password|passwd|secret|token|key)")


@dataclass
class Redactions:
    counts: dict[str, int] = field(default_factory=dict)

    def add(self, kind: str, n: int = 1) -> None:
        self.counts[kind] = self.counts.get(kind, 0) + n


def redact_text(text: str, r: Redactions) -> str:
    for kind, pat in PATTERNS:

        def repl(m: re.Match[str], kind: str = kind) -> str:
            r.add(kind)
            if kind == "url-credentials":
                return f"{m.group(1)}:{REDACTED}@"
            if kind == "key-value":
                return f"{m.group(1)}={REDACTED}"
            return REDACTED

        text = pat.sub(repl, text)
    return text


def redact_object(obj: object, r: Redactions) -> object:
    """Redact a Kubernetes object: Secret data, sensitive env values, then free text."""
    if isinstance(obj, dict):
        if obj.get("kind") == "Secret":
            out = dict(obj)
            for key in ("data", "stringData"):
                if key in out and isinstance(out[key], dict):
                    r.add("secret-data", len(out[key]))
                    out[key] = dict.fromkeys(out[key], REDACTED)
            return {k: redact_object(v, r) if k not in ("data", "stringData") else v for k, v in out.items()}
        if "name" in obj and "value" in obj and SENSITIVE_ENV.search(str(obj["name"])):
            r.add("env")
            return {**obj, "value": REDACTED}
        if obj.get("kind") == "ConfigMap" and "last-applied" in json.dumps(obj.get("metadata", {})):
            obj = {**obj, "metadata": {k: v for k, v in obj["metadata"].items() if k != "annotations"}}
        return {k: redact_object(v, r) for k, v in obj.items()}
    if isinstance(obj, list):
        return [redact_object(v, r) for v in obj]
    if isinstance(obj, str):
        return redact_text(obj, r)
    return obj


def redact_file(name: str, content: str, r: Redactions) -> str:
    if name.endswith((".yaml", ".yml")):
        docs = [redact_object(d, r) for d in yaml.safe_load_all(content) if d is not None]
        return yaml.safe_dump_all(docs, sort_keys=False)
    if name.endswith(".json"):
        return json.dumps(redact_object(json.loads(content), r), indent=2) + "\n"
    return redact_text(content, r)


def build(source: Path, out: Path) -> Redactions:
    """Redact every file under `source` (kubectl output collected at the site) into a .tar.gz."""
    r = Redactions()
    with tarfile.open(out, "w:gz") as tar:
        files = sorted(p for p in source.rglob("*") if p.is_file())
        for p in files:
            rel = p.relative_to(source).as_posix()
            data = redact_file(rel, p.read_text(encoding="utf-8", errors="replace"), r).encode()
            info = tarfile.TarInfo(f"bundle/{rel}")
            info.size, info.mtime, info.mode = len(data), 0, 0o644
            tar.addfile(info, io.BytesIO(data))
        manifest = json.dumps(
            {"files": [p.relative_to(source).as_posix() for p in files], "redactions": r.counts},
            indent=2,
            sort_keys=True,
        ).encode()
        info = tarfile.TarInfo("bundle/REDACTIONS.json")
        info.size, info.mtime, info.mode = len(manifest), 0, 0o644
        tar.addfile(info, io.BytesIO(manifest))
    return r
