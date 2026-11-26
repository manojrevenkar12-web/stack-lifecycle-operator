from pathlib import Path

from stackctl import gate

RULES = gate.Rules.load(Path("../../config/review-gate.yaml").read_text())
AI = "Fix rollback\n\nCo-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>\n"
REVIEWED = AI + "Reviewed-by: Anna Weber <anna.weber@example.com>\n"


def changes(*items):
    return [gate.Change(p, s) for s, p in items]


def test_human_change_to_docs_passes():
    v = gate.evaluate(changes(("M", "README.md")), "Update docs\n", RULES)
    assert v.ok and not v.ai_assisted


def test_ai_change_to_controller_needs_reviewer_and_test():
    v = gate.evaluate(changes(("M", "internal/controller/platformstack_controller.go")), AI, RULES)
    assert v.ai_assisted and not v.ok
    assert len(v.problems) == 2


def test_ai_change_with_reviewer_and_test_passes():
    c = changes(
        ("M", "internal/controller/platformstack_controller.go"),
        ("M", "internal/controller/platformstack_controller_test.go"),
    )
    v = gate.evaluate(c, REVIEWED, RULES)
    assert v.ok, v.problems
    assert "Passed." in v.markdown()


def test_ai_trailer_counts_as_assisted():
    v = gate.evaluate(changes(("M", "config/rbac/role.yaml")), "Tighten role\n\nAI-Assisted: yes\n", RULES)
    assert v.ai_assisted and not v.ok


def test_deleting_tests_and_rewriting_heldout_are_flagged():
    v = gate.evaluate(changes(("D", "internal/plan/plan_test.go"), ("M", "results/heldout.json")), "cleanup\n", RULES)
    assert len(v.problems) == 2


def test_first_heldout_run_may_add_the_lock():
    v = gate.evaluate(changes(("A", "results/heldout.json"), ("A", "results/HELDOUT.lock")), "held-out run\n", RULES)
    assert v.ok


def test_rbac_wildcard_and_secret_listing_in_diff():
    diff = (
        '+// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch\n'
        "+//+kubebuilder:rbac:groups=*,resources=*,verbs=*\n"
    )
    v = gate.evaluate(changes(("M", "internal/controller/x.go")), REVIEWED, RULES, diff)
    assert any("secrets" in p for p in v.problems) and any("wildcard" in p for p in v.problems)


def test_hardcoded_credential_in_diff():
    v = gate.evaluate([], "x\n", RULES, '+  password: "correct-horse-battery"\n')
    assert any("credential" in p for p in v.problems)


def test_parse_name_status():
    out = gate.parse_name_status("M\tREADME.md\nR087\told.go\tnew.go\nD\tgone_test.go\n")
    assert [(c.status, c.path) for c in out] == [("M", "README.md"), ("R", "new.go"), ("D", "gone_test.go")]
