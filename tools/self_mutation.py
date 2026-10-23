"""keel's own mutation test: apply one fault at a time to a copy of keel.py and
require the suite to go red. A check whose removal no test notices is a check
nothing verifies. Run after changing any keel check:

    python3 tools/self_mutation.py

Lives outside tools/tests on purpose: it is a checker, not a test, and it re-runs the whole suite per mutant (slow).
Add a mutant here whenever you add a check to keel.py."""
import shutil, subprocess, sys, tempfile
from pathlib import Path

src = Path(sys.argv[1]) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1]
MUTANTS = [
    ("zero-selected accepted as red", "    if s.selected == 0:\n        raise KeelError(f\"{where}: the filter selected 0 tests; a missing", "    if False:\n        raise KeelError(f\"{where}: the filter selected 0 tests; a missing"),
    ("passing test accepted as red", "    if code == 0:\n        raise KeelError(f\"{where}: the test PASSED", "    if False:\n        raise KeelError(f\"{where}: the test PASSED"),
    ("loader errors accepted", "    if errors:\n        return TestSummary(0, 0, \"errors", "    if False:\n        return TestSummary(0, 0, \"errors"),
    ("green check skipped", "    if code != 0 or s.invalid or s.failed:", "    if False:"),
    ("inject may edit tests", "        if bad:\n            raise KeelError(f\"line {inj.line}: an injected fault", "        if False:\n            raise KeelError(f\"line {inj.line}: an injected fault"),
    ("placeholder lint off", "    if started and PLACEHOLDER in card.text:", "    if False:"),
    ("done without gate ok", "        if not any(verdict == \"ok\" for _, verdict, _ in card.gates):", "        if False:"),
    ("unchecked criterion ok", "        if not item.checks:", "        if False:"),
    ("freeze change ignored", "            elif new.frozen_text() != old.frozen_text():", "            elif False:"),
    ("assert drop ignored", "        if s[\"assert-\"] > s[\"assert+\"]:", "        if False:"),
    ("skip ignored", "        if s[\"skip+\"]:", "        if False:"),
    ("early exit ignored", "        if s[\"suspicious+\"]:", "        if False:"),
    ("zero-test gate green", "            if s.selected == 0:\n                code, extra", "            if False:\n                code, extra"),
    ("boundary ignored", "                    if re.search(pat, line):\n                        probs.append(f\"{f}:{i}: [", "                    if False:\n                        probs.append(f\"{f}:{i}: ["),
    ("registry symbol ignored", "            elif not symbol_exists(root, files, tok):", "            elif False:"),
    ("comment claims parsed", "            if in_comment:\n                in_comment = \"-->\" not in line\n                continue", "            if False:\n                pass"),
    ("legacy growth ignored", "        if grown:\n            problems.append(", "        if False:\n            problems.append("),
    ("stale legacy entry ignored", "    if stale and not args.cards:", "    if False:"),
    ("legacy cards linted", "    return [p for p in all_card_paths(root, cfg) if p.name not in legacy]", "    return all_card_paths(root, cfg)"),
    ("adopt lists compliant cards", "    legacy = [p.name for p in cards if lint_card(load_card(root, p))]", "    legacy = [p.name for p in cards]"),
    ("profile ignored", "    return {**base, **profiles[profile]}", "    return base"),
    ("profile cwd ignored", "    cwd = tree / red.get(\"cwd\", \".\")", "    cwd = tree"),
    ("args not split", "                cmd += shlex.split(filt)", "                cmd.append(filt)"),
    ("decisions unscoped", "        body = section_text(plan.read_text(encoding=\"utf-8\"), d[\"decisions_heading\"])", "        body = plan.read_text(encoding=\"utf-8\")"),
    ("absolute link allowed", "            if path.startswith(\"/\"):", "            if False:"),
    ("inline code linked", "        prose = re.sub(r\"`[^`\\n]*`\", \"\", re.sub(", "        prose = (lambda x: x)(re.sub("),
]
survived = []
for name, old, new in MUTANTS:
    work = Path(tempfile.mkdtemp(prefix="keel-mut-"))
    shutil.copytree(src, work / "keel", ignore=shutil.ignore_patterns(".git", ".keel", "__pycache__"))
    k = work / "keel" / "tools" / "keel.py"
    text = k.read_text()
    assert text.count(old) == 1, f"anchor not unique/missing: {name}"
    k.write_text(text.replace(old, new))
    p = subprocess.run([sys.executable, "-m", "unittest", "discover", "-s", "tools/tests"], cwd=work / "keel", capture_output=True, text=True)
    status = "killed" if p.returncode != 0 else "SURVIVED"
    if p.returncode == 0: survived.append(name)
    print(f"{status:9} {name}")
    shutil.rmtree(work)
print(f"\n{len(MUTANTS) - len(survived)}/{len(MUTANTS)} killed; survived: {survived}")
sys.exit(1 if survived else 0)
