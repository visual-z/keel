"""keel's own tests. The checker is code too: every check here is shown
catching the violation it exists for, not only passing on clean input.

Each test builds a throwaway git repository with a tiny Python project and
drives keel through its real CLI entry point.
"""

from __future__ import annotations

import contextlib
import io
import os
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))
import keel  # noqa: E402

PY = sys.executable

KEEL_TOML = f"""
[project]
name = "fixture"

[[gate.step]]
name = "unit"
cmd = [{PY!r}, "-m", "unittest", "discover", "-s", "."]
runner = "unittest"

[red]
runner = "unittest"
command = [{PY!r}, "-m", "unittest", "discover", "-s", ".", "-k", "{{filter}}"]
timeout_s = 120

[tests]
paths = ["test_*.py"]
assert_patterns = ['\\bassert\\w*\\(', '\\bassert\\b']
skip_patterns = ['unittest\\.skip', '\\.skipTest\\(']
suspicious_patterns = ['\\bsys\\.exit\\(', 'def __eq__']

[guard]
base = "main"

[docs]
agents_max_lines = 20
playbook_max_rules = 3
glossary_source_dirs = ["src"]

[[boundary.rule]]
name = "core is pure"
paths = ["src/core/**"]
forbid = ['^import (os|time)\\b']
"""

BUGGY = "def add(a, b):\n    return a - b\n"
FIXED = "def add(a, b):\n    return a + b\n"
TEST = textwrap.dedent("""\
    import unittest
    from calc import add


    class Calc(unittest.TestCase):
        def test_adds_two_numbers(self):
            self.assertEqual(add(2, 2), 4)

        def test_adds_zero(self):
            self.assertEqual(add(0, 0), 0)
""")


def card_text(*, status="doing", type_="fix", weight="light", body_extra="", dod=None, evidence=""):
    dod = dod or "- **D1** add returns the sum.\n  - 检：test `test_adds_two_numbers`\n"
    return (f"---\ntype: {type_}\nstatus: {status}\nweight: {weight}\ncreated: 2026-10-23\n---\n\n"
            f"# fix — add\n\n## 问题\n\nadd subtracts.\n\n## 完成定义\n\n{dod}\n{body_extra}"
            f"## 证据\n\n{evidence}\n")


class Repo:
    def __init__(self):
        self.tmp = tempfile.TemporaryDirectory(prefix="keel-test-")
        self.root = Path(self.tmp.name)
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.invalid")
        self.git("config", "user.name", "t")
        self.git("config", "commit.gpgsign", "false")
        self.write("keel.toml", KEEL_TOML)
        self.write(".gitignore", ".keel/\n__pycache__/\n")

    def write(self, rel, text):
        p = self.root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")

    def git(self, *args):
        p = subprocess.run(["git", *args], cwd=self.root, capture_output=True, text=True)
        if p.returncode != 0:
            raise AssertionError(f"git {args} failed: {p.stderr}")
        return p.stdout.strip()

    def commit(self, msg="c"):
        self.git("add", "-A")
        self.git("commit", "-q", "-m", msg)
        return self.git("rev-parse", "HEAD")

    def keel(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = keel.main(["--root", str(self.root), *args])
        return code, out.getvalue() + err.getvalue()

    def close(self):
        self.git("worktree", "prune")
        self.tmp.cleanup()


class RepoCase(unittest.TestCase):
    def setUp(self):
        self.r = Repo()
        self.addCleanup(self.r.close)

    def assertKeelFails(self, args, needle):
        code, out = self.r.keel(*args)
        self.assertEqual(code, 1, out)
        self.assertIn(needle, out)

    def assertKeelPasses(self, args):
        code, out = self.r.keel(*args)
        self.assertEqual(code, 0, out)
        return out


class VerifyRed(RepoCase):
    def red_history(self):
        """main: buggy code + new test (the RED commit), then the fix."""
        self.r.write("calc.py", BUGGY)
        self.r.write("test_calc.py", TEST)
        red = self.r.commit("test: pin add")
        self.r.write("calc.py", FIXED)
        self.r.commit("fix: add adds")
        return red

    def test_a_test_red_before_and_green_after_the_fix_verifies(self):
        red = self.red_history()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=f"RED: {red} test_adds_two_numbers  # D1\n"))
        out = self.assertKeelPasses(["verify-red", "docs/tasks/fix-add.md"])
        self.assertIn("1 RED claim(s)", out)
        self.assertIn(f"refs/keel/red/{red}", self.r.git("for-each-ref", "refs/keel/red"))

    def test_a_filter_that_selects_no_test_is_not_red_evidence(self):
        red = self.red_history()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=f"RED: {red} test_typo_name\n"))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "selected 0 tests")

    def test_a_test_that_passes_at_the_red_commit_is_rejected(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST)
        sha = self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=f"RED: {sha} test_adds_two_numbers\n"))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "PASSED")

    def test_an_import_error_is_not_a_behavioural_red(self):
        self.r.write("calc.py", "import does_not_exist\n")
        self.r.write("test_calc.py", TEST)
        sha = self.r.commit()
        self.r.write("calc.py", FIXED)
        self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=f"RED: {sha} test_adds\n"))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "not a behavioural red")

    def test_a_real_failure_next_to_a_loader_error_still_does_not_certify(self):
        # Only the loader-error rule can reject this run: one test genuinely
        # fails, so "non-zero exit without a failed test" does not apply.
        self.r.write("calc.py", BUGGY)
        self.r.write("test_calc.py", TEST)
        self.r.write("test_broken.py", "import does_not_exist\n")
        sha = self.r.commit()
        self.r.write("calc.py", FIXED)
        (self.r.root / "test_broken.py").unlink()
        self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=f"RED: {sha} test\n"))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "import or setup failures")

    def test_a_card_whose_fix_is_still_red_fails_the_green_run(self):
        self.r.write("calc.py", BUGGY)
        self.r.write("test_calc.py", TEST)
        sha = self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=f"RED: {sha} test_adds_two_numbers\n"))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "expected green")

    def test_an_injected_fault_in_product_code_must_turn_the_test_red(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST)
        self.r.commit()
        patch = ("--- a/calc.py\n+++ b/calc.py\n@@ -1,2 +1,2 @@\n def add(a, b):\n"
                 "-    return a + b\n+    return a - b\n")
        body = f"```diff keel-inject test_adds\n{patch}```\n"
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=body))
        out = self.assertKeelPasses(["verify-red", "docs/tasks/fix-add.md"])
        self.assertIn("injected fault", out)
        self.assertEqual(self.r.git("status", "--porcelain", "--", "calc.py"), "")

    def test_an_injected_fault_the_tests_cannot_see_is_reported(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST)
        self.r.commit()
        # a * b agrees with a + b on (2, 2) and (0, 0): the suite cannot tell.
        patch = ("--- a/calc.py\n+++ b/calc.py\n@@ -1,2 +1,2 @@\n def add(a, b):\n"
                 "-    return a + b\n+    return a * b if a == b else a + b\n")
        body = f"```diff keel-inject test_adds\n{patch}```\n"
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=body))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "PASSED")

    def test_an_injected_fault_may_not_edit_the_tests(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST)
        self.r.commit()
        patch = ("--- a/test_calc.py\n+++ b/test_calc.py\n@@ -6,2 +6,2 @@\n     def test_adds_two_numbers(self):\n"
                 "-        self.assertEqual(add(2, 2), 4)\n+        self.assertEqual(add(2, 2), 5)\n")
        body = f"```diff keel-inject test_adds\n{patch}```\n"
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=body))
        self.assertKeelFails(["verify-red", "docs/tasks/fix-add.md"], "not tests")

    def test_snapshot_commits_new_tests_on_top_of_head_without_moving_the_branch(self):
        self.r.write("calc.py", BUGGY)
        self.r.write("test_calc.py", "")
        head = self.r.commit()
        self.r.write("test_calc.py", TEST)
        code, out = self.r.keel("snapshot", "test_calc.py")
        self.assertEqual(code, 0, out)
        snap = out.strip().splitlines()[-1]
        self.assertEqual(self.r.git("rev-parse", "HEAD"), head)
        self.assertEqual(self.r.git("rev-parse", f"{snap}^"), head)
        self.assertIn("test_adds_two_numbers", self.r.git("show", f"{snap}:test_calc.py"))
        self.assertIn(" M test_calc.py", subprocess.run(["git", "status", "--porcelain"], cwd=self.r.root,
                                                        capture_output=True, text=True).stdout)
        self.assertEqual(self.r.git("diff", "--cached", "--name-only"), "")

    def test_audit_red_reports_a_claim_whose_commit_is_gone(self):
        self.r.write("calc.py", BUGGY)
        self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text(evidence="RED: deadbeef1234 test_adds\n"))
        self.assertKeelFails(["audit-red"], "missing")


class Lint(RepoCase):
    def lint(self, text, name="fix-add.md"):
        self.r.write(f"docs/tasks/{name}", text)
        return self.r.keel("lint", f"docs/tasks/{name}")

    def test_a_well_formed_light_card_passes(self):
        code, out = self.lint(card_text())
        self.assertEqual(code, 0, out)

    def test_a_full_card_missing_required_sections_fails(self):
        code, out = self.lint(card_text(weight="full"))
        self.assertEqual(code, 1)
        for sec in ("观测面", "环境假设", "范围"):
            self.assertIn(sec, out)

    def test_a_criterion_without_a_check_fails(self):
        code, out = self.lint(card_text(dod="- **D1** add works.\n"))
        self.assertEqual(code, 1)
        self.assertIn("nothing checks", out)

    def test_placeholders_left_in_a_started_card_fail(self):
        code, out = self.lint(card_text(dod="- **D1** ⟨判据⟩\n  - 检：test `x`\n"))
        self.assertEqual(code, 1)
        self.assertIn("placeholders", out)

    def test_a_done_fix_without_red_or_gate_receipt_fails(self):
        code, out = self.lint(card_text(status="done"))
        self.assertEqual(code, 1)
        self.assertIn("RED", out)
        self.assertIn("GATE", out)

    def test_red_none_is_refused_on_a_behaviour_card(self):
        code, out = self.lint(card_text(evidence="RED: none — it is obvious\n"))
        self.assertEqual(code, 1)
        self.assertIn("not allowed", out)

    def test_an_unconfirmed_author_check_blocks_done(self):
        dod = "- **D1** the dialog looks right.\n  - 检：author 打开设置看一眼\n"
        ev = "RED: abcdef1 x\nGATE: abc ok unit=0\n"
        code, out = self.lint(card_text(status="done", dod=dod, evidence=ev))
        self.assertEqual(code, 1)
        self.assertIn("awaiting-author", out)
        dod2 = "- **D1** the dialog looks right.\n  - 检：author 打开设置看一眼 已确认：2026-10-23\n"
        code, out = self.lint(card_text(status="done", dod=dod2, evidence=ev))
        self.assertEqual(code, 0, out)

    def test_file_name_must_match_the_type(self):
        code, out = self.lint(card_text(), name="feat-add.md")
        self.assertEqual(code, 1)
        self.assertIn("file name", out)

    def test_red_lines_inside_code_fences_and_comments_are_not_claims(self):
        card = keel.parse_card(card_text(body_extra="```\nRED: abcdef1 not-a-claim\n```\n"
                                         "<!--\nRED: <commit> <filter>\n-->\n"), "x.md")
        self.assertEqual((card.reds, card.errors), ([], []))

    def test_the_shipped_example_card_lints_clean(self):
        root = Path(__file__).resolve().parent.parent.parent
        text = (root / "docs/examples/fix-example.md").read_text(encoding="utf-8")
        card = keel.parse_card(text, "docs/tasks/fix-example.md")
        self.assertEqual(keel.lint_card(card), [])
        self.assertEqual((len(card.reds), len(card.injects), len(card.items)), (1, 1, 4))

    def test_a_card_fresh_from_the_real_template_has_only_placeholder_problems(self):
        root = Path(__file__).resolve().parent.parent.parent
        for name in ("TEMPLATE.md", "TEMPLATE-LIGHT.md"):
            text = (root / "docs/tasks" / name).read_text(encoding="utf-8")
            text = text.replace("type: ⟨feat|fix|perf|refactor|debt|docs|chore|research|review|release⟩", "type: fix")
            text = text.replace("type: ⟨fix|chore|docs|debt⟩", "type: fix")
            card = keel.parse_card(text, "docs/tasks/fix-x.md")
            self.assertEqual(card.errors, [], name)
            self.assertEqual(keel.lint_card(card), [], name)  # status todo: placeholders allowed
            card.meta["status"] = "doing"
            self.assertTrue(any("placeholders" in p for p in keel.lint_card(card)), name)


class Freeze(RepoCase):
    def test_changing_a_frozen_criterion_without_author_confirmation_fails(self):
        self.r.write("docs/tasks/fix-add.md", card_text(status="todo"))
        self.r.commit("open card")
        loose = "- **D1** add returns roughly the sum.\n  - 检：test `test_adds_two_numbers`\n"
        self.r.write("docs/tasks/fix-add.md", card_text(dod=loose))
        self.assertKeelFails(["freeze-check"], "D1 changed")

    def test_an_author_confirmed_revision_is_accepted(self):
        self.r.write("docs/tasks/fix-add.md", card_text(status="todo"))
        self.r.commit("open card")
        loose = "- **D1** add returns the sum of ints.\n  - 检：test `test_adds_two_numbers`\n"
        rev = "## 修订\n\n- D1 限定为整数：浮点另开卡（作者确认 2026-10-23）\n\n"
        self.r.write("docs/tasks/fix-add.md", card_text(dod=loose, body_extra=rev))
        self.assertKeelPasses(["freeze-check"])

    def test_adding_a_criterion_and_confirming_an_author_check_are_not_changes(self):
        dod = "- **D1** add returns the sum.\n  - 检：author 看一眼\n"
        self.r.write("docs/tasks/fix-add.md", card_text(status="todo", dod=dod))
        self.r.commit("open card")
        dod2 = dod.replace("看一眼", "看一眼 已确认：2026-10-24") + "- **D2** zero.\n  - 检：test `test_adds_zero`\n"
        self.r.write("docs/tasks/fix-add.md", card_text(dod=dod2))
        self.assertKeelPasses(["freeze-check"])

    def test_a_started_card_that_was_never_committed_has_no_freeze_point(self):
        self.r.write("calc.py", BUGGY)
        self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text(status="doing"))
        self.assertKeelFails(["freeze-check"], "never committed")

    def test_a_full_card_first_committed_as_done_has_no_freeze_point(self):
        self.r.write("docs/tasks/fix-add.md", card_text(status="done", weight="full"))
        self.r.commit()
        self.assertKeelFails(["freeze-check"], "freeze point")


class AssertGuard(RepoCase):
    def base(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST)
        self.r.commit()
        self.r.git("checkout", "-q", "-b", "work")

    def test_removing_an_assertion_is_flagged(self):
        self.base()
        self.r.write("test_calc.py", TEST.replace("        self.assertEqual(add(0, 0), 0)\n", "        pass\n"))
        self.assertKeelFails(["assert-guard"], "assertions dropped")

    def test_adding_a_skip_is_flagged(self):
        self.base()
        self.r.write("test_calc.py", TEST.replace("    def test_adds_zero", "    @unittest.skip('flaky')\n    def test_adds_zero"))
        self.assertKeelFails(["assert-guard"], "skip")

    def test_exiting_before_the_asserts_is_flagged(self):
        self.base()
        self.r.write("test_calc.py", TEST.replace("        self.assertEqual(add(2, 2), 4)",
                                                   "        import sys; sys.exit(0)\n        self.assertEqual(add(2, 2), 4)"))
        self.assertKeelFails(["assert-guard"], "early-exit")

    def test_deleting_a_test_file_is_flagged(self):
        self.base()
        (self.r.root / "test_calc.py").unlink()
        self.assertKeelFails(["assert-guard"], "deleted")

    def test_a_removed_line_that_starts_with_dashes_is_not_a_file_header(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST + "# -- a/notes\n")
        self.r.commit()
        self.r.git("checkout", "-q", "-b", "work")
        self.r.write("test_calc.py", TEST + "        self.assertTrue(True)\n")
        self.assertKeelPasses(["assert-guard"])

    def test_an_author_confirmed_weakening_is_allowed(self):
        self.base()
        self.r.write("test_calc.py", TEST.replace("        self.assertEqual(add(0, 0), 0)\n", "        pass\n"))
        ok = "WEAKEN-OK: test_calc.py 零值用例并入属性测试（作者确认 2026-10-23）\n"
        self.r.write("docs/tasks/fix-add.md", card_text(evidence=ok))
        self.assertKeelPasses(["assert-guard"])

    def test_adding_assertions_passes(self):
        self.base()
        self.r.write("test_calc.py", TEST + "        self.assertEqual(add(1, 2), 3)\n")
        self.assertKeelPasses(["assert-guard"])


class Gate(RepoCase):
    def test_a_green_gate_prints_counts_and_records_a_receipt(self):
        self.r.write("calc.py", FIXED)
        self.r.write("test_calc.py", TEST)
        self.r.commit()
        self.r.write("docs/tasks/fix-add.md", card_text())
        out = self.assertKeelPasses(["gate", "--record", "docs/tasks/fix-add.md"])
        self.assertIn("unit=0[2p/0f]", out)
        text = (self.r.root / "docs/tasks/fix-add.md").read_text()
        self.assertRegex(text, r"## 证据\n\nGATE: [0-9a-f]{12}\+dirty ok unit=0")

    def test_a_red_gate_fails_and_says_so_in_the_receipt(self):
        self.r.write("calc.py", BUGGY)
        self.r.write("test_calc.py", TEST)
        self.r.commit()
        self.assertKeelFails(["gate"], " fail unit=1")

    def test_a_test_step_that_runs_nothing_is_not_green(self):
        self.r.write("README.md", "x\n")
        self.r.commit()
        code, out = self.r.keel("gate")
        self.assertEqual(code, 1, out)
        self.assertIn("0 tests", out)


class Docs(RepoCase):
    def test_an_oversized_agents_map_fails(self):
        self.r.write("AGENTS.md", "x\n" * 30)
        self.assertKeelFails(["check-docs"], "is a map")

    def test_a_broken_link_and_a_stale_map_path_fail(self):
        self.r.write("AGENTS.md", "see [plan](PLAN.md) and `docs/GONE.md`\n")
        self.r.write("docs/x.md", "x\n")
        self.r.commit()
        code, out = self.r.keel("check-docs")
        self.assertEqual(code, 1)
        self.assertIn("broken link", out)
        self.assertIn("docs/GONE.md", out)

    def test_decision_numbers_must_be_unique_and_increasing(self):
        self.r.write("AGENTS.md", "x\n")
        self.r.write("PLAN.md", "| # | d |\n|---|---|\n| 1 | a |\n| 3 | b |\n| 2 | c |\n")
        self.r.commit()
        self.assertKeelFails(["check-docs"], "decision numbers")

    def test_an_invariant_pointing_at_nothing_fails(self):
        self.r.write("AGENTS.md", "x\n")
        self.r.write("calc.py", FIXED)
        self.r.write("docs/INVARIANTS.md", "| ID | 不变量 | 检查方式 | 位置 |\n|---|---|---|---|\n"
                     "| INV-1 | sum | property | `test_add_is_commutative` |\n"
                     "| INV-2 | sum | example | `calc.py` |\n")
        self.r.commit()
        self.assertKeelFails(["check-docs"], "test_add_is_commutative")

    def test_a_term_the_glossary_avoids_is_found_in_source(self):
        self.r.write("AGENTS.md", "x\n")
        self.r.write("CONTEXT.md", "| 术语 | 定义 | 避免 |\n|---|---|---|\n| Node | 机器 | `Server`、`Host` |\n")
        self.r.write("src/app.py", "class Server: pass\n")
        self.r.commit()
        self.assertKeelFails(["check-docs"], "uses `Server`")


class Boundaries(RepoCase):
    def test_a_forbidden_import_in_core_is_reported_and_clean_core_passes(self):
        self.r.write("src/core/pure.py", "def f():\n    return 1\n")
        self.r.commit()
        self.assertKeelPasses(["check-boundaries"])
        self.r.write("src/core/pure.py", "import os\n")
        self.assertKeelFails(["check-boundaries"], "core is pure")


class Init(RepoCase):
    def test_init_turns_the_template_into_a_named_project(self):
        real = Path(__file__).resolve().parent.parent.parent
        for rel in ("tools/presets/cargo.toml", "README.md", "README.zh-CN.md", "README.project.md",
                    "AGENTS.md", "PLAN.md", "CONTEXT.md", "docs/PROGRESS.md"):
            self.r.write(rel, (real / rel).read_text(encoding="utf-8"))
        self.assertKeelPasses(["init", "--name", "demo", "--stack", "cargo"])
        root = self.r.root
        self.assertFalse((root / "README.zh-CN.md").exists())
        self.assertFalse((root / "README.project.md").exists())
        self.assertTrue((root / "README.md").read_text(encoding="utf-8").startswith("# demo"))
        self.assertIn('name = "demo"', (root / "keel.toml").read_text(encoding="utf-8"))
        for rel in ("AGENTS.md", "PLAN.md", "CONTEXT.md"):
            self.assertNotIn("{{PROJECT_NAME}}", (root / rel).read_text(encoding="utf-8"), rel)
        self.assertEqual(self.r.git("config", "core.hooksPath"), ".githooks")

    def test_init_rejects_an_unknown_stack(self):
        self.r.write("tools/presets/cargo.toml", "")
        self.assertKeelFails(["init", "--name", "demo", "--stack", "cobol"], "available: cargo")


class Runners(unittest.TestCase):
    def test_cargo_compile_errors_are_not_red_and_counts_sum_across_binaries(self):
        self.assertIsNotNone(keel.parse_cargo("error[E0425]: cannot find value").invalid)
        out = ("running 2 tests\ntest result: FAILED. 1 passed; 1 failed; 0 ignored\n"
               "running 3 tests\ntest result: ok. 3 passed; 0 failed; 0 ignored\n")
        s = keel.parse_cargo(out)
        self.assertEqual((s.selected, s.failed, s.invalid), (5, 1, None))

    def test_go_build_failures_are_not_red(self):
        self.assertIsNotNone(keel.parse_go("FAIL\tex/pkg [build failed]\n").invalid)
        s = keel.parse_go("=== RUN   TestA\n--- FAIL: TestA (0.00s)\n=== RUN   TestB\n--- PASS: TestB\n")
        self.assertEqual((s.selected, s.failed), (2, 1))

    def test_pytest_collection_errors_are_not_red(self):
        self.assertIsNotNone(keel.parse_pytest("1 error in 0.10s\n").invalid)
        s = keel.parse_pytest("1 failed, 2 passed in 0.12s\n")
        self.assertEqual((s.selected, s.failed), (3, 1))

    def test_bun_vitest_and_jest_summaries_parse(self):
        self.assertEqual(keel.parse_bun(" 1 pass\n 2 fail\n").failed, 2)
        self.assertIsNotNone(keel.parse_bun(" 0 pass\n 1 fail\n 1 error\n").invalid)
        self.assertEqual(keel.parse_vitest("      Tests  1 failed | 2 passed (3)\n").selected, 3)
        self.assertEqual(keel.parse_jest("Tests:       1 failed, 1 skipped, 2 passed, 4 total\n").selected, 3)
        self.assertIsNotNone(keel.parse_jest("Test suite failed to run\n").invalid)


if __name__ == "__main__":
    unittest.main()
