import json
import tarfile
from pathlib import Path

from stackctl import bundle

SITE = Path(__file__).parent / "fixtures" / "site"


def read_bundle(path):
    with tarfile.open(path) as tar:
        return {m.name: tar.extractfile(m).read().decode() for m in tar.getmembers()}


def test_bundle_leaks_nothing_and_lists_redactions(tmp_path):
    out = tmp_path / "b.tar.gz"
    r = bundle.build(SITE, out)
    files = read_bundle(out)
    everything = "\n".join(files.values())
    for secret in [
        "c3VwZXItc2VjcmV0LXBhc3N3b3Jk",
        "hunter2",
        "s3cr3t-pass",
        "rabbit-pass",
        "abcd1234efgh",
        "eyJhbGciOiJSUzI1NiJ9",
        "MIIEowIBAAKCAQEA",
        "23456789",
        "262011234567890",
        "ops@customer.example",
    ]:
        assert secret not in everything, secret
    # what is needed to diagnose stays
    assert "migration step 17" in everything
    assert "RETENTION_DAYS" in everything
    manifest = json.loads(files["bundle/REDACTIONS.json"])
    assert manifest["redactions"] == r.counts
    assert r.counts["secret-data"] == 2 and r.counts["env"] == 1


def test_bundle_is_reproducible(tmp_path):
    a, b = tmp_path / "a.tgz", tmp_path / "b.tgz"
    bundle.build(SITE, a)
    bundle.build(SITE, b)
    assert read_bundle(a) == read_bundle(b)


def test_text_redaction_keeps_structure():
    r = bundle.Redactions()
    out = bundle.redact_text("db=postgres://u:pw@h/db password=abc", r)
    assert out == "db=postgres://u:[REDACTED]@h/db password=[REDACTED]"
