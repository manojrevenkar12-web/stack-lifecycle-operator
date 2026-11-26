"""Review gate for AI-assisted changes.

Code written with an AI assistant goes through the same review as any other code, plus a few
rules that are cheap to check mechanically. The model never decides whether a change is safe; this
gate only decides what a person must look at before it merges:

* a change marked AI-assisted (an ``AI-Assisted: yes`` trailer or a ``Co-Authored-By`` trailer
  naming an assistant) that touches a security-sensitive path needs a ``Reviewed-by:`` trailer from
  a named person and a test change in the same set of commits;
* nobody, assisted or not, deletes a test file or weakens RBAC without a reviewer;
* the held-out lab results are never rewritten.
"""

from __future__ import annotations

import fnmatch
import re
from dataclasses import dataclass, field

import yaml

ASSISTANT = re.compile(r"^co-authored-by:.*\b(claude|copilot|chatgpt|gpt|gemini|codex)\b", re.I | re.M)
AI_TRAILER = re.compile(r"^ai-assisted:\s*(yes|true)\s*$", re.I | re.M)
REVIEWED = re.compile(r"^reviewed-by:\s*\S+.*<[^>]+@[^>]+>\s*$", re.I | re.M)


@dataclass
class Rules:
    sensitive: list[str]
    tests: list[str]
    locked: list[str]

    @classmethod
    def load(cls, text: str) -> Rules:
        raw = yaml.safe_load(text) or {}
        return cls(raw.get("sensitive", []), raw.get("tests", []), raw.get("locked", []))


@dataclass
class Change:
    path: str
    status: str  # A, M, D, R


@dataclass
class Verdict:
    ai_assisted: bool
    reviewed: bool
    sensitive: list[str] = field(default_factory=list)
    problems: list[str] = field(default_factory=list)

    @property
    def ok(self) -> bool:
        return not self.problems

    def markdown(self) -> str:
        lines = ["### Review gate", ""]
        lines.append(f"- AI-assisted: {'yes' if self.ai_assisted else 'no'}")
        lines.append(f"- Named reviewer trailer: {'yes' if self.reviewed else 'no'}")
        if self.sensitive:
            lines.append("- Security-sensitive files: " + ", ".join(f"`{p}`" for p in self.sensitive))
        lines.append("")
        if self.ok:
            lines.append("Passed.")
        else:
            lines.append("Needs attention:")
            lines += [f"- {p}" for p in self.problems]
        return "\n".join(lines) + "\n"


def _match(path: str, patterns: list[str]) -> bool:
    return any(fnmatch.fnmatch(path, p) for p in patterns)


def evaluate(changes: list[Change], messages: str, rules: Rules, diff: str = "") -> Verdict:
    ai = bool(ASSISTANT.search(messages) or AI_TRAILER.search(messages))
    reviewed = bool(REVIEWED.search(messages))
    v = Verdict(ai_assisted=ai, reviewed=reviewed)
    v.sensitive = sorted({c.path for c in changes if _match(c.path, rules.sensitive)})
    touched_tests = any(_match(c.path, rules.tests) and c.status != "D" for c in changes)
    for c in changes:
        if c.status == "D" and _match(c.path, rules.tests) and not reviewed:
            v.problems.append(f"`{c.path}` deletes tests; a named reviewer must sign off (Reviewed-by:)")
        if _match(c.path, rules.locked) and c.status != "A":
            v.problems.append(f"`{c.path}` is locked; held-out results are written once by `lab sim --split heldout`")
    if ai and v.sensitive:
        if not reviewed:
            v.problems.append("AI-assisted change to security-sensitive files needs a Reviewed-by: trailer")
        if not touched_tests:
            v.problems.append("AI-assisted change to security-sensitive files must add or change a test")
    for line in diff.splitlines():
        if not line.startswith("+") or line.startswith("+++"):
            continue
        added = line[1:]
        if re.search(r"verbs=\*|verbs:\s*\[\s*\"?\*", added) or "resources=*" in added:
            v.problems.append(f"RBAC wildcard added: `{added.strip()}`")
        if re.search(r"resources=secrets.*verbs=.*\b(list|watch)\b", added):
            v.problems.append(f"list/watch on secrets added: `{added.strip()}`")
        if re.search(r"(?i)(password|secret|token)\s*[:=]\s*\"[^\"]{8,}\"", added) and "randomSecret" not in added:
            v.problems.append(f"possible hard-coded credential: `{added.strip()[:80]}`")
    return v


def parse_name_status(text: str) -> list[Change]:
    out = []
    for line in text.splitlines():
        parts = line.split("\t")
        if len(parts) >= 2:
            status = parts[0][0]
            out.append(Change(parts[-1], status))
    return out
