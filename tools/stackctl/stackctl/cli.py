"""stackctl: openshift-lint, review-gate, support-bundle."""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

from . import bundle, gate, lint


def _git(*args: str) -> str:
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def cmd_lint(a: argparse.Namespace) -> int:
    text = sys.stdin.read() if a.file == "-" else Path(a.file).read_text()
    findings = lint.lint(lint.load_documents(text))
    if a.json:
        print(json.dumps([f.__dict__ for f in findings], indent=2))
    else:
        for f in findings:
            print(f)
    errors = [f for f in findings if f.severity == "error"]
    warnings = len(findings) - len(errors)
    print(f"{len(errors)} errors, {warnings} warnings", file=sys.stderr)
    return 1 if errors or (a.strict and findings) else 0


def cmd_gate(a: argparse.Namespace) -> int:
    rules = gate.Rules.load(Path(a.rules).read_text())
    rng = f"{a.base}..{a.head}"
    changes = gate.parse_name_status(_git("diff", "--name-status", "-M", rng))
    messages = _git("log", "--format=%B%n---", rng)
    diff = _git("diff", "-U0", rng)
    v = gate.evaluate(changes, messages, rules, diff)
    out = v.markdown()
    print(out)
    if a.summary:
        with open(a.summary, "a", encoding="utf-8") as f:
            f.write(out)
    return 0 if v.ok else 1


def cmd_bundle(a: argparse.Namespace) -> int:
    r = bundle.build(Path(a.source), Path(a.out))
    total = sum(r.counts.values())
    print(f"wrote {a.out}; {total} redactions: " + ", ".join(f"{k} {v}" for k, v in sorted(r.counts.items())))
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(prog="stackctl")
    sub = p.add_subparsers(dest="cmd", required=True)

    pl = sub.add_parser("openshift-lint", help="check rendered manifests against restricted-v2 and RBAC rules")
    pl.add_argument("file", help="multi-document YAML, or - for stdin")
    pl.add_argument("--json", action="store_true")
    pl.add_argument("--strict", action="store_true", help="fail on warnings too")
    pl.set_defaults(fn=cmd_lint)

    pg = sub.add_parser("review-gate", help="check a commit range against the AI-assisted change rules")
    pg.add_argument("--base", required=True)
    pg.add_argument("--head", default="HEAD")
    pg.add_argument("--rules", default="config/review-gate.yaml")
    pg.add_argument("--summary", help="append the verdict as markdown (e.g. $GITHUB_STEP_SUMMARY)")
    pg.set_defaults(fn=cmd_gate)

    pb = sub.add_parser("support-bundle", help="redact collected diagnostics into a bundle safe to share")
    pb.add_argument("source", help="directory of kubectl output collected at the site")
    pb.add_argument("--out", default="support-bundle.tar.gz")
    pb.set_defaults(fn=cmd_bundle)

    a = p.parse_args(argv)
    return a.fn(a)


if __name__ == "__main__":
    raise SystemExit(main())
