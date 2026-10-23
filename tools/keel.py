#!/usr/bin/env python3
"""keel — the workspace's trusted kernel.

Every claim a task card makes ("this test was red before the fix", "the
definition of done was not relaxed", "the gate passed") is a claim like any
other. keel re-derives each one from git and from real exit codes instead of
believing the narrative, the same way a proof checker re-checks a proof
instead of trusting whoever wrote it.

Design constraints (see docs/PRINCIPLES.md):
  - Standard library only, one file: the trusted kernel must stay small
    enough to read, and must run in any project regardless of its stack.
  - Stack knowledge lives in keel.toml (data), never in this file (code).
  - Fail closed: anything keel cannot classify is a failure, not a pass.

Usage: python3 tools/keel.py <command> [...]   (see docs/KEEL.md)
"""

from __future__ import annotations

import argparse
import datetime as _dt
import fnmatch
import os
import re
import shutil
import subprocess
import sys
import tempfile
from dataclasses import dataclass, field
from pathlib import Path

if sys.version_info < (3, 11):
    sys.exit("keel needs Python 3.11+ (tomllib)")
import tomllib  # noqa: E402

CARD_TYPES = ("feat", "fix", "perf", "refactor", "debt", "docs", "chore", "research", "review", "release")
# Types whose whole point is a behaviour change. They must carry RED evidence:
# a test that cannot fail proves nothing about the change.
BEHAVIOUR_TYPES = ("feat", "fix", "perf")
STATUSES = ("todo", "doing", "blocked", "awaiting-author", "done")
WEIGHTS = ("full", "light")
CHECK_KINDS = ("test", "property", "inject", "cmd", "grep", "author", "measure")
FULL_SECTIONS = ("来源", "范围", "观测面", "环境假设", "完成定义", "设计", "验证命令", "证据")
LIGHT_SECTIONS = ("问题", "完成定义", "证据")
# Unambiguous placeholder brackets: `<T>` is legal code, ⟨…⟩ never is.
PLACEHOLDER = "⟨"
RED_REF_PREFIX = "refs/keel/red/"
HEX = re.compile(r"^[0-9a-f]{7,64}$")
DATE = r"\d{4}-\d{2}-\d{2}"


class KeelError(Exception):
    """A check failed. Printed as FAIL and exits 1."""


# ---------------------------------------------------------------- utilities

def run(cmd: list[str], cwd: Path, *, input: str | None = None, env: dict | None = None,
        timeout: float | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(cmd, cwd=cwd, input=input, env=env, timeout=timeout,
                          capture_output=True, text=True, errors="replace")


def git(root: Path, *args: str, input: str | None = None, env: dict | None = None) -> subprocess.CompletedProcess:
    return run(["git", *args], root, input=input, env=env)


def git_ok(root: Path, *args: str) -> str:
    p = git(root, *args)
    if p.returncode != 0:
        raise KeelError(f"git {' '.join(args)} failed: {p.stderr.strip()}")
    return p.stdout.strip()


def strip_ansi(s: str) -> str:
    return re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", s)


def glob_match(path: str, patterns: list[str]) -> bool:
    # fnmatch's `*` already crosses `/`; `**/` must also match zero segments.
    for pat in patterns:
        if fnmatch.fnmatch(path, pat) or (pat.startswith("**/") and fnmatch.fnmatch(path, pat[3:])):
            return True
    return False


def tracked_files(root: Path) -> list[str]:
    p = git(root, "ls-files", "--cached", "--others", "--exclude-standard")
    if p.returncode != 0:
        return [str(f.relative_to(root)) for f in root.rglob("*") if f.is_file() and ".git" not in f.parts]
    return [line for line in p.stdout.splitlines() if line]


def find_root(explicit: str | None) -> Path:
    if explicit:
        return Path(explicit).resolve()
    if os.environ.get("KEEL_ROOT"):
        return Path(os.environ["KEEL_ROOT"]).resolve()
    p = run(["git", "rev-parse", "--show-toplevel"], Path.cwd())
    if p.returncode == 0:
        return Path(p.stdout.strip())
    return Path(__file__).resolve().parent.parent


# ------------------------------------------------------------------- config

DEFAULT_CONFIG: dict = {
    "project": {"name": "project", "stack": "custom"},
    "paths": {"tasks": "docs/tasks"},
    "gate": {"step": []},
    "red": {"runner": "custom", "command": [], "link": [], "env": {}, "timeout_s": 1800},
    "tests": {"paths": [], "inline_paths": [], "assert_patterns": [], "skip_patterns": [],
              "suspicious_patterns": []},
    "guard": {"base": "main"},
    "docs": {"agents_max_lines": 150, "playbook_max_rules": 120, "glossary_source_dirs": []},
    "mutate": {"command": []},
    "boundary": {"rule": []},
}


def load_config(root: Path) -> dict:
    path = root / "keel.toml"
    data = tomllib.loads(path.read_text(encoding="utf-8")) if path.exists() else {}
    cfg: dict = {}
    for key, default in DEFAULT_CONFIG.items():
        merged = dict(default)
        merged.update(data.get(key, {}))
        cfg[key] = merged
    return cfg


# -------------------------------------------------------------------- cards

@dataclass
class Check:
    kind: str
    arg: str
    line: int
    confirmed: str | None  # date an author confirmed a hand check


@dataclass
class Item:
    id: int
    line: int
    lines: list[str] = field(default_factory=list)
    checks: list[Check] = field(default_factory=list)

    def frozen_text(self) -> str:
        """The text that must not change after the freeze point. Author
        confirmations are appended later by design and are not a change."""
        text = "\n".join(self.lines)
        text = re.sub(rf"\s*已确认[：:]\s*{DATE}", "", text)
        return re.sub(r"\s+", " ", text).strip()


@dataclass
class Red:
    revision: str
    filter: str
    line: int


@dataclass
class Inject:
    filter: str
    patch: str
    line: int


@dataclass
class Card:
    rel: str
    text: str
    meta: dict
    sections: list[str]
    items: dict[int, Item]
    reds: list[Red]
    red_none: list[tuple[str, int]]
    injects: list[Inject]
    gates: list[tuple[str, str, int]]
    revised: set[int]
    weaken_ok: list[str]
    errors: list[str]


D_LINE = re.compile(r"^- \*\*D(\d+)\*\*\s*(.*)$")
CHECK_LINE = re.compile(r"^\s+- 检[：:]\s*(\w+)\s*(.*)$")
RED_LINE = re.compile(r"^RED:\s+(\S+)\s+(.+?)\s*$")
GATE_LINE = re.compile(r"^GATE:\s+(\S+)\s+(ok|fail)\b(.*)$")
INJECT_OPEN = re.compile(r"^```diff\s+keel-inject\s+(.+?)\s*$")
WEAKEN_LINE = re.compile(rf"^WEAKEN-OK:\s+(\S+)\s+.*作者确认\s*{DATE}")


def parse_frontmatter(lines: list[str]) -> tuple[dict, int]:
    if not lines or lines[0].strip() != "---":
        return {}, 0
    meta: dict = {}
    for i in range(1, len(lines)):
        line = lines[i]
        if line.strip() == "---":
            return meta, i + 1
        m = re.match(r"^([A-Za-z_][\w-]*)\s*:\s*(.*)$", line)
        if m:
            value = re.sub(r"\s+#.*$", "", m.group(2)).strip().strip('"').strip("'")
            meta[m.group(1)] = value
    return meta, 0


def parse_card(text: str, rel: str) -> Card:
    lines = text.splitlines()
    meta, start = parse_frontmatter(lines)
    card = Card(rel, text, meta, [], {}, [], [], [], [], set(), [], [])
    fence: str | None = None   # None, "code", or "inject"
    inject_filter, inject_body, inject_line = "", [], 0
    section = ""
    current: Item | None = None
    in_comment = False  # HTML comments hold format help; their examples are not claims
    for idx in range(start, len(lines)):
        n, line = idx + 1, lines[idx]
        if not fence:
            if in_comment:
                in_comment = "-->" not in line
                continue
            if line.lstrip().startswith("<!--"):
                in_comment = "-->" not in line
                continue
        if fence:
            if line.strip() == "```":
                if fence == "inject":
                    card.injects.append(Inject(inject_filter, "\n".join(inject_body) + "\n", inject_line))
                fence = None
            elif fence == "inject":
                inject_body.append(line)
            continue
        m = INJECT_OPEN.match(line)
        if m:
            fence, inject_filter, inject_body, inject_line = "inject", m.group(1), [], n
            continue
        if line.startswith("```"):
            fence = "code"
            continue
        h = re.match(r"^## (.+?)\s*$", line)
        if h:
            section = h.group(1)
            card.sections.append(section)
            current = None
            continue
        if section.startswith("完成定义"):
            dm = D_LINE.match(line)
            if dm:
                did = int(dm.group(1))
                if did in card.items:
                    card.errors.append(f"line {n}: D{did} is defined twice")
                current = Item(did, n, [line.strip()])
                card.items[did] = current
                continue
            cm = CHECK_LINE.match(line)
            if cm and current:
                arg = cm.group(2)
                conf = re.search(rf"已确认[：:]\s*({DATE})", arg)
                current.checks.append(Check(cm.group(1), arg, n, conf.group(1) if conf else None))
                current.lines.append(line.strip())
                continue
            if current and line.startswith("  ") and line.strip():
                current.lines.append(line.strip())
                continue
            if line.strip() == "":
                continue
            current = None
        if section.startswith("修订"):
            for rid in re.findall(r"\bD(\d+)\b", line) if "作者确认" in line else []:
                card.revised.add(int(rid))
        rm = RED_LINE.match(line)
        if rm:
            rev, rest = rm.group(1), rm.group(2)
            if rev == "none":
                card.red_none.append((rest, n))
            elif HEX.match(rev):
                filt = re.sub(r"\s+#\s*D\d+(?:\s*,\s*D\d+)*\s*$", "", rest).strip()
                card.reds.append(Red(rev, filt, n))
            else:
                card.errors.append(f"line {n}: RED revision {rev!r} is not a commit id")
            continue
        gm = GATE_LINE.match(line)
        if gm:
            card.gates.append((gm.group(1), gm.group(2), n))
            continue
        wm = WEAKEN_LINE.match(line)
        if wm:
            card.weaken_ok.append(wm.group(1))
    if fence == "inject":
        card.errors.append(f"line {inject_line}: keel-inject block is not closed")
    return card


def card_paths(root: Path, cfg: dict) -> list[Path]:
    tasks = root / cfg["paths"]["tasks"]
    if not tasks.is_dir():
        return []
    return sorted(p for p in tasks.glob("*.md") if not p.name.startswith(("TEMPLATE", "README")))


def load_card(root: Path, path: str | Path) -> Card:
    p = Path(path)
    p = p if p.is_absolute() else root / p
    if not p.exists():
        raise KeelError(f"{path} does not exist")
    rel = str(p.resolve().relative_to(root.resolve())) if p.resolve().is_relative_to(root.resolve()) else str(p)
    return parse_card(p.read_text(encoding="utf-8"), rel)


def lint_card(card: Card) -> list[str]:
    probs = list(card.errors)
    meta = card.meta
    if not meta:
        return probs + ["missing front matter (--- type/status/weight/created ---)"]
    for key in ("type", "status", "weight", "created"):
        if not meta.get(key):
            probs.append(f"front matter is missing `{key}`")
    ctype, status, weight = meta.get("type", ""), meta.get("status", ""), meta.get("weight", "")
    if ctype and ctype not in CARD_TYPES:
        probs.append(f"type `{ctype}` is not one of {', '.join(CARD_TYPES)}")
    if status and status not in STATUSES:
        probs.append(f"status `{status}` is not one of {', '.join(STATUSES)}")
    if weight and weight not in WEIGHTS:
        probs.append(f"weight `{weight}` is not one of {', '.join(WEIGHTS)}")
    name = Path(card.rel).name
    if ctype in CARD_TYPES and not re.match(rf"^{ctype}-[a-z0-9]+(?:-[a-z0-9]+)*\.md$", name):
        probs.append(f"file name must be `{ctype}-<kebab-slug>.md`")
    if status == "blocked" and not meta.get("reason"):
        probs.append("a blocked card needs `reason:` in front matter")
    required = FULL_SECTIONS if weight == "full" else LIGHT_SECTIONS
    for sec in required:
        if not any(s.startswith(sec) for s in card.sections):
            probs.append(f"missing section `## {sec}`")
    if not card.items:
        probs.append("完成定义 has no `- **D1** …` items")
    for item in card.items.values():
        if not item.checks:
            probs.append(f"D{item.id} has no `- 检：<kind> …` line: a criterion nothing checks is a wish")
        for c in item.checks:
            if c.kind not in CHECK_KINDS:
                probs.append(f"line {c.line}: check kind `{c.kind}` is not one of {', '.join(CHECK_KINDS)}")
            if c.kind in ("test", "property", "grep", "cmd") and "`" not in c.arg:
                probs.append(f"line {c.line}: `{c.kind}` check must name its target in backticks")
    started = status in ("doing", "awaiting-author", "done", "blocked")
    if started and PLACEHOLDER in card.text:
        probs.append("template placeholders ⟨…⟩ are still present")
    if card.red_none and ctype in BEHAVIOUR_TYPES:
        probs.append(f"`RED: none` is not allowed on a `{ctype}` card: behaviour changes need a red test")
    for reason, n in card.red_none:
        if len(reason.strip(" —-")) < 4:
            probs.append(f"line {n}: `RED: none` needs a reason")
    finished = status in ("awaiting-author", "done")
    if finished and ctype in BEHAVIOUR_TYPES and not (card.reds or card.injects):
        probs.append(f"a finished `{ctype}` card needs a `RED: <commit> <filter>` line or a keel-inject block")
    if status == "done":
        if not any(verdict == "ok" for _, verdict, _ in card.gates):
            probs.append("a done card needs a `GATE: <rev> ok …` receipt (python3 tools/keel.py gate --record <card>)")
        for item in card.items.values():
            for c in item.checks:
                if c.kind == "author" and not c.confirmed:
                    probs.append(f"D{item.id}: author check is not confirmed (`已确认：YYYY-MM-DD`); "
                                 "use status awaiting-author until the author confirms")
    return probs


# ------------------------------------------------------------------ runners

@dataclass
class TestSummary:
    selected: int
    failed: int
    invalid: str | None  # why the output cannot certify anything


def _ints(pattern: str, text: str) -> list[int]:
    return [int(m) for m in re.findall(pattern, text, re.M)]


def parse_cargo(out: str) -> TestSummary:
    if re.search(r"^error(\[E\d+\])?:|could not compile", out, re.M):
        return TestSummary(0, 0, "compile error (a compile error is not a behavioural red)")
    return TestSummary(sum(_ints(r"^running (\d+) tests?$", out)),
                       sum(_ints(r"^test result: \w+\. \d+ passed; (\d+) failed", out)), None)


def parse_go(out: str) -> TestSummary:
    if re.search(r"\[(build|setup) failed\]|^# \S+\n.*\.go:\d+", out, re.M):
        return TestSummary(0, 0, "build failed (a compile error is not a behavioural red)")
    return TestSummary(len(re.findall(r"^=== RUN\s", out, re.M)),
                       len(re.findall(r"^\s*--- FAIL: ", out, re.M)), None)


def parse_bun(out: str) -> TestSummary:
    if re.search(r"^\s*\d+ errors?\s*$", out, re.M) or "Unhandled error between tests" in out:
        return TestSummary(0, 0, "load or unhandled errors")
    passed, failed = _ints(r"^\s*(\d+) pass\s*$", out), _ints(r"^\s*(\d+) fail\s*$", out)
    return TestSummary(sum(passed) + sum(failed), sum(failed), None)


def parse_pytest(out: str) -> TestSummary:
    lines = [l for l in out.splitlines() if re.search(r"\b(passed|failed|error|errors|no tests ran)\b.* in [\d.]+s", l)]
    if not lines:
        return TestSummary(0, 0, None)
    last = lines[-1]
    passed, failed = sum(_ints(r"(\d+) passed", last)), sum(_ints(r"(\d+) failed", last))
    if _ints(r"(\d+) errors?\b", last):
        return TestSummary(0, 0, "collection or fixture errors")
    return TestSummary(passed + failed, failed, None)


def parse_vitest(out: str) -> TestSummary:
    if re.search(r"Unhandled Errors?|^\s*Errors\s+\d+ errors?", out, re.M):
        return TestSummary(0, 0, "unhandled errors")
    m = re.findall(r"^\s*Tests\s+(.*)$", out, re.M)
    if not m:
        return TestSummary(0, 0, None)
    last = m[-1]
    failed = sum(_ints(r"(\d+) failed", last))
    passed = sum(_ints(r"(\d+) passed", last))
    return TestSummary(passed + failed, failed, None)


def parse_jest(out: str) -> TestSummary:
    if "Test suite failed to run" in out:
        return TestSummary(0, 0, "a test suite failed to load")
    m = re.findall(r"^Tests:\s+(.*)$", out, re.M)
    if not m:
        return TestSummary(0, 0, None)
    last = m[-1]
    failed = sum(_ints(r"(\d+) failed", last))
    passed = sum(_ints(r"(\d+) passed", last))
    return TestSummary(passed + failed, failed, None)


def parse_unittest(out: str) -> TestSummary:
    ran = _ints(r"^Ran (\d+) tests? in", out)
    fm = re.search(r"^FAILED \((.*)\)\s*$", out, re.M)
    failed = errors = 0
    if fm:
        failed = sum(_ints(r"failures=(\d+)", fm.group(1)))
        errors = sum(_ints(r"errors=(\d+)", fm.group(1)))
    if errors:
        return TestSummary(0, 0, "errors (import or setup failures are not a behavioural red)")
    return TestSummary(sum(ran), failed, None)


RUNNERS = {"cargo": parse_cargo, "go": parse_go, "bun": parse_bun, "pytest": parse_pytest,
           "vitest": parse_vitest, "jest": parse_jest, "unittest": parse_unittest}


def summarize(cfg: dict, runner: str, out: str) -> TestSummary:
    out = strip_ansi(out)
    if runner in RUNNERS:
        return RUNNERS[runner](out)
    red = cfg["red"]
    if runner == "custom" and red.get("selected_re") and red.get("failed_re"):
        if red.get("invalid_re") and re.search(red["invalid_re"], out, re.M):
            return TestSummary(0, 0, "matched invalid_re")
        return TestSummary(sum(_ints(red["selected_re"], out)), sum(_ints(red["failed_re"], out)), None)
    raise KeelError(f"unknown runner `{runner}`: set red.runner, or red.selected_re/failed_re for `custom`")


def test_env(root: Path, cfg: dict) -> dict:
    cache = root / ".keel" / "cache"
    cache.mkdir(parents=True, exist_ok=True)
    env = dict(os.environ)
    # Coloured summaries break the count parsers and turn a real red into "matched 0".
    env.update({"NO_COLOR": "1", "FORCE_COLOR": "0", "CARGO_TERM_COLOR": "never", "CI": env.get("CI", "1")})
    for k, v in cfg["red"].get("env", {}).items():
        env[k] = str(v).replace("{cache}", str(cache))
    return env


def run_filter(root: Path, cfg: dict, cwd: Path, filt: str) -> tuple[int | None, TestSummary, str]:
    template = cfg["red"]["command"]
    if not template or not any("{filter}" in a for a in template):
        raise KeelError("red.command in keel.toml must contain a `{filter}` argument")
    cmd = [a.replace("{filter}", filt) for a in template]
    try:
        p = run(cmd, cwd, env=test_env(root, cfg), timeout=float(cfg["red"].get("timeout_s", 1800)))
    except subprocess.TimeoutExpired:
        return None, TestSummary(0, 0, "timed out"), ""
    except FileNotFoundError:
        raise KeelError(f"cannot run `{cmd[0]}`")
    out = p.stdout + p.stderr
    return p.returncode, summarize(cfg, cfg["red"]["runner"], out), out


def tail(out: str, n: int = 12) -> str:
    return "\n".join("      " + l for l in strip_ansi(out).rstrip().splitlines()[-n:])


def require_red(code: int | None, s: TestSummary, out: str, where: str) -> None:
    if code is None:
        raise KeelError(f"{where}: the test run timed out; a timeout is not a red")
    if code < 0:
        raise KeelError(f"{where}: the runner was killed by signal {-code}; a crash is not a red")
    if s.invalid:
        raise KeelError(f"{where}: {s.invalid}\n{tail(out)}")
    if s.selected == 0:
        raise KeelError(f"{where}: the filter selected 0 tests; a missing test is not red evidence\n{tail(out)}")
    if code == 0:
        raise KeelError(f"{where}: the test PASSED; it does not detect what the card claims")
    if s.failed == 0:
        raise KeelError(f"{where}: non-zero exit without a failed test (exit {code}); not a behavioural red\n{tail(out)}")


def require_green(code: int | None, s: TestSummary, out: str, where: str) -> None:
    if code != 0 or s.invalid or s.failed:
        raise KeelError(f"{where}: expected green after the fix, got exit {code}"
                        f"{' (' + s.invalid + ')' if s.invalid else ''}\n{tail(out)}")
    if s.selected == 0:
        raise KeelError(f"{where}: the filter selected 0 tests at the fixed tree")


# --------------------------------------------------------------- worktrees

class Worktree:
    """A throwaway checkout, removed on every exit path (including errors)."""

    def __init__(self, root: Path, cfg: dict, rev: str):
        self.root, self.cfg, self.rev = root, cfg, rev
        self.path = Path(tempfile.mkdtemp(prefix="keel-red-"))

    def __enter__(self) -> Path:
        p = git(self.root, "worktree", "add", "--detach", "--quiet", str(self.path), self.rev)
        if p.returncode != 0:
            shutil.rmtree(self.path, ignore_errors=True)
            raise KeelError(f"could not create a worktree at {self.rev}: {p.stderr.strip()}")
        for name in self.cfg["red"].get("link", []):
            src, dst = self.root / name, self.path / name
            if src.exists() and not dst.exists():
                dst.symlink_to(src, target_is_directory=src.is_dir())
        return self.path

    def __exit__(self, *exc) -> None:
        for name in self.cfg["red"].get("link", []):
            dst = self.path / name
            if dst.is_symlink():
                dst.unlink()  # never let a recursive delete walk into the real directory
        git(self.root, "worktree", "remove", "--force", str(self.path))
        shutil.rmtree(self.path, ignore_errors=True)
        git(self.root, "worktree", "prune")


def resolve_commit(root: Path, rev: str) -> str | None:
    if not HEX.match(rev) and rev != "HEAD":
        return None
    p = git(root, "rev-parse", "--verify", "--quiet", f"{rev}^{{commit}}")
    return p.stdout.strip() if p.returncode == 0 else None


def red_ref_state(root: Path, sha: str) -> str:
    p = git(root, "rev-parse", "--verify", "--quiet", RED_REF_PREFIX + sha)
    if p.returncode != 0:
        return "missing"
    return "retained" if p.stdout.strip() == sha else "conflict"


def retain(root: Path, sha: str) -> None:
    """Pin a RED commit so a squash or a pruned reflog cannot erase the evidence.
    A create transaction refuses existing refs; never move a pointer to go green."""
    state = red_ref_state(root, sha)
    if state == "retained":
        return
    if state == "conflict":
        raise KeelError(f"{RED_REF_PREFIX}{sha} exists and points elsewhere")
    git(root, "update-ref", "--stdin", input=f"option no-deref\ncreate {RED_REF_PREFIX}{sha} {sha}\n")
    if red_ref_state(root, sha) != "retained":
        raise KeelError(f"could not retain {sha}")


def patch_paths(patch: str) -> list[str]:
    paths = set(re.findall(r"^\+\+\+ b/(\S+)", patch, re.M)) | set(re.findall(r"^--- a/(\S+)", patch, re.M))
    return sorted(paths)


# ---------------------------------------------------------------- commands

def cmd_verify_red(root: Path, cfg: dict, args) -> None:
    card = load_card(root, args.card)
    if card.errors:
        raise KeelError("; ".join(card.errors))
    if not card.reds and not card.injects:
        raise KeelError(f"{card.rel} has no `RED: <commit> <filter>` line and no keel-inject block")
    filters: list[str] = []
    for red in card.reds:
        sha = resolve_commit(root, red.revision)
        if not sha:
            raise KeelError(f"line {red.line}: RED commit {red.revision} does not exist in this repository")
        print(f"RED     {sha[:12]}  {red.filter}")
        with Worktree(root, cfg, sha) as wt:
            code, s, out = run_filter(root, cfg, wt, red.filter)
            require_red(code, s, out, f"line {red.line}")
        retain(root, sha)
        print(f"        red: {s.failed}/{s.selected} selected tests failed, exit {code}")
        filters.append(red.filter)
    base = resolve_commit(root, args.at or "HEAD")
    if card.injects and not base:
        raise KeelError(f"cannot resolve {args.at or 'HEAD'}")
    for inj in card.injects:
        touched = patch_paths(inj.patch)
        if not touched:
            raise KeelError(f"line {inj.line}: keel-inject block is not a unified diff")
        test_paths = cfg["tests"]["paths"]
        bad = [p for p in touched if glob_match(p, test_paths)]
        if bad:
            raise KeelError(f"line {inj.line}: an injected fault must break product code, not tests: {', '.join(bad)}")
        print(f"INJECT  {base[:12]}+patch  {inj.filter}  ({', '.join(touched)})")
        with Worktree(root, cfg, base) as wt:
            p = git(wt, "apply", "--whitespace=nowarn", "-", input=inj.patch)
            if p.returncode != 0:
                raise KeelError(f"line {inj.line}: the fault patch does not apply to {base[:12]}: {p.stderr.strip()}")
            code, s, out = run_filter(root, cfg, wt, inj.filter)
            require_red(code, s, out, f"line {inj.line}")
        print(f"        red: {s.failed}/{s.selected} selected tests failed under the injected fault")
        filters.append(inj.filter)
    if args.no_green:
        print("GREEN   skipped (--no-green)")
    else:
        for filt in dict.fromkeys(filters):
            code, s, out = run_filter(root, cfg, root, filt)
            require_green(code, s, out, f"green `{filt}`")
            print(f"GREEN   working tree  {filt}  ({s.selected} selected, all pass)")
    print(f"ok      {len(card.reds)} RED claim(s), {len(card.injects)} injected fault(s) verified")


def cmd_audit_red(root: Path, cfg: dict, args) -> None:
    problems, claims = [], 0
    for path in card_paths(root, cfg):
        card = load_card(root, path)
        problems += [f"{card.rel}: {e}" for e in card.errors]
        for red in card.reds:
            claims += 1
            sha = resolve_commit(root, red.revision)
            if not sha:
                problems.append(f"{card.rel}:{red.line}: commit {red.revision} is missing")
                continue
            state = red_ref_state(root, sha)
            if state == "missing" and args.retain:
                retain(root, sha)
            elif state != "retained":
                problems.append(f"{card.rel}:{red.line}: {sha[:12]} is {state} (run audit-red --retain)")
    print(f"audit-red: {claims} claim(s), {len(problems)} problem(s) (tests not re-run; use verify-red)")
    if problems:
        raise KeelError("\n".join(problems))


def cmd_lint(root: Path, cfg: dict, args) -> None:
    paths = [Path(p) for p in args.cards] if args.cards else card_paths(root, cfg)
    failed = 0
    for path in paths:
        card = load_card(root, path)
        probs = lint_card(card)
        if probs:
            failed += 1
            print(f"FAIL  {card.rel}")
            for p in probs:
                print(f"      - {p}")
    print(f"lint: {len(paths)} card(s), {failed} with problems")
    if failed:
        raise KeelError("card lint failed")


def first_committed(root: Path, rel: str) -> tuple[str, str] | None:
    p = git(root, "log", "--diff-filter=A", "--follow", "--format=%H", "--", rel)
    shas = p.stdout.split()
    if p.returncode != 0 or not shas:
        return None
    sha = shas[-1]
    shown = git(root, "show", f"{sha}:{rel}")
    if shown.returncode != 0:  # renamed since: read whatever path it had then
        names = git(root, "show", "--name-only", "--format=", sha).stdout.split()
        name = next((n for n in names if n.endswith(Path(rel).name)), rel)
        shown = git(root, "show", f"{sha}:{name}")
    return sha, shown.stdout


def cmd_freeze_check(root: Path, cfg: dict, args) -> None:
    paths = [Path(p) for p in args.cards] if args.cards else card_paths(root, cfg)
    problems = []
    for path in paths:
        card = load_card(root, path)
        status, weight = card.meta.get("status"), card.meta.get("weight")
        first = first_committed(root, card.rel)
        if first is None:
            if status not in (None, "todo"):
                problems.append(f"{card.rel}: status `{status}` but the card was never committed; "
                                "commit the card (the freeze point) before implementing")
            continue
        sha, old_text = first
        frozen = parse_card(old_text, card.rel)
        if frozen.meta.get("status") == "done" and weight == "full":
            problems.append(f"{card.rel}: first committed already `done` at {sha[:12]}; a full card needs "
                            "a freeze point committed before the implementation")
        for did, old in frozen.items.items():
            new = card.items.get(did)
            if did in card.revised:
                continue
            if new is None:
                problems.append(f"{card.rel}: D{did} was removed after the freeze at {sha[:12]} "
                                "without an author-confirmed entry in `## 修订`")
            elif new.frozen_text() != old.frozen_text():
                problems.append(f"{card.rel}: D{did} changed after the freeze at {sha[:12]} "
                                "without an author-confirmed entry in `## 修订`")
    print(f"freeze-check: {len(paths)} card(s), {len(problems)} problem(s)")
    if problems:
        raise KeelError("\n".join(problems))


def merge_base(root: Path, base: str) -> str:
    for ref in (base, f"origin/{base}"):
        p = git(root, "merge-base", ref, "HEAD")
        if p.returncode == 0:
            return p.stdout.strip()
    raise KeelError(f"cannot find a merge base with `{base}` (set guard.base or pass --base)")


def cmd_assert_guard(root: Path, cfg: dict, args) -> None:
    """Agents most often 'finish' by weakening the tests: deleting assertions,
    skipping tests, or exiting before the asserts run. Count them in the diff."""
    t = cfg["tests"]
    scope = t["paths"] + t.get("inline_paths", [])
    if not scope:
        raise KeelError("tests.paths is empty in keel.toml; the guard cannot know what a test is")
    if not resolve_commit(root, "HEAD"):
        print("assert-guard: no commit yet, nothing to compare against")
        return
    mb = merge_base(root, args.base or cfg["guard"]["base"])
    allowed: set[str] = set()
    for path in card_paths(root, cfg):
        allowed.update(load_card(root, path).weaken_ok)
    diff = git(root, "diff", "--no-color", "--unified=0", "--no-renames", mb, "--").stdout
    per_file: dict[str, dict[str, int]] = {}
    current, in_header = None, False
    for line in diff.splitlines():
        # File headers only occur between `diff --git` and the first `@@`; a
        # removed line whose content starts with `--` must not be read as one.
        if line.startswith("diff --git "):
            current, in_header = None, True
            continue
        if in_header:
            if line.startswith("--- a/"):
                current = line[6:]
            elif line.startswith("+++ b/"):
                current = line[6:]
            elif line.startswith("@@"):
                in_header = False
            continue
        if line.startswith("@@"):
            continue
        if current is None or not line or line[0] not in "+-" or not glob_match(current, scope):
            continue
        stats = per_file.setdefault(current, {"assert+": 0, "assert-": 0, "skip+": 0, "suspicious+": 0})
        sign, body = line[0], line[1:]
        if any(re.search(p, body) for p in t["assert_patterns"]):
            stats["assert" + sign] += 1
        if sign == "+" and any(re.search(p, body) for p in t["skip_patterns"]):
            stats["skip+"] += 1
        if sign == "+" and any(re.search(p, body) for p in t["suspicious_patterns"]):
            stats["suspicious+"] += 1
    deleted = git(root, "diff", "--name-only", "--diff-filter=D", mb, "--").stdout.split()
    problems = []
    for f in deleted:
        if glob_match(f, t["paths"]) and f not in allowed:
            problems.append(f"{f}: test file deleted")
    for f, s in sorted(per_file.items()):
        if f in allowed:
            continue
        if s["assert-"] > s["assert+"]:
            problems.append(f"{f}: assertions dropped ({s['assert-']} removed, {s['assert+']} added)")
        if s["skip+"]:
            problems.append(f"{f}: {s['skip+']} skip/only marker(s) added")
        if s["suspicious+"]:
            problems.append(f"{f}: {s['suspicious+']} early-exit or always-equal pattern(s) added")
    print(f"assert-guard: diff against {mb[:12]}, {len(per_file)} test file(s) touched, {len(problems)} problem(s)")
    if problems:
        raise KeelError("\n".join(problems) + "\n      If intended, the card must say "
                        "`WEAKEN-OK: <path> <reason>（作者确认 YYYY-MM-DD）`")


def head_label(root: Path) -> str:
    sha = git(root, "rev-parse", "--short=12", "HEAD").stdout.strip() or "unborn"
    dirty = git(root, "status", "--porcelain").stdout.strip()
    return sha + ("+dirty" if dirty else "")


def cmd_gate(root: Path, cfg: dict, args) -> None:
    steps = cfg["gate"]["step"]
    if not steps:
        raise KeelError("keel.toml has no [[gate.step]]; a gate with nothing in it passes vacuously")
    logs = root / ".keel" / "logs"
    logs.mkdir(parents=True, exist_ok=True)
    stamp = _dt.datetime.now().strftime("%Y%m%d-%H%M%S")
    results, ok = [], True
    for step in steps:
        name, cmd = step["name"], step["cmd"]
        if args.only and name not in args.only:
            continue
        log = logs / f"{stamp}-{name}.log"
        print(f"gate  {name:<12} {' '.join(cmd)}", flush=True)
        start = _dt.datetime.now()
        try:
            with log.open("w", encoding="utf-8") as fh:
                code = subprocess.run(cmd, cwd=root, stdout=fh, stderr=subprocess.STDOUT,
                                      env=test_env(root, cfg), timeout=step.get("timeout_s")).returncode
        except FileNotFoundError:
            code = 127
        except subprocess.TimeoutExpired:
            code = 124
        secs = (_dt.datetime.now() - start).total_seconds()
        extra = ""
        if step.get("runner") and code is not None:
            s = summarize(cfg, step["runner"], log.read_text(encoding="utf-8", errors="replace"))
            extra = f"[{s.selected - s.failed}p/{s.failed}f]"
            if s.selected == 0:
                code, extra = code or 1, "[0 tests: a test step that runs nothing is not green]"
        results.append(f"{name}={code}{extra}")
        print(f"      exit {code} {extra} {secs:.1f}s  log: {log.relative_to(root)}")
        if code != 0:
            ok = False
            if not args.keep_going:
                break
    receipt = f"GATE: {head_label(root)} {'ok' if ok else 'fail'} {' '.join(results)} ({_dt.date.today()})"
    print(receipt)
    if args.record:
        record_receipt(root, args.record, receipt)
    if not ok:
        raise KeelError("gate failed")


def record_receipt(root: Path, card_path: str, line: str) -> None:
    p = Path(card_path)
    p = p if p.is_absolute() else root / p
    text = p.read_text(encoding="utf-8")
    lines = text.splitlines()
    idx = next((i for i, l in enumerate(lines) if l.startswith("## 证据")), None)
    if idx is None:
        lines = [*lines, "", "## 证据", ""]
        idx = len(lines) - 2
    end = next((j for j in range(idx + 1, len(lines)) if lines[j].startswith("## ")), len(lines))
    body = lines[idx + 1:end]
    while body and not body[-1].strip():
        body.pop()
    while body and not body[0].strip():
        body.pop(0)
    rest = lines[end:]
    lines = [*lines[:idx + 1], "", *body, line, *([""] + rest if rest else [])]
    p.write_text("\n".join(lines) + "\n", encoding="utf-8")
    print(f"recorded in {card_path}")


LINK = re.compile(r"\]\(([^)\s]+)\)")
BACKTICK = re.compile(r"`([^`\s]+)`")


def cmd_check_docs(root: Path, cfg: dict, args) -> None:
    d = cfg["docs"]
    probs: list[str] = []
    agents = root / "AGENTS.md"
    if not agents.exists():
        probs.append("AGENTS.md is missing")
    else:
        n = len(agents.read_text(encoding="utf-8").splitlines())
        if n > int(d["agents_max_lines"]):
            probs.append(f"AGENTS.md has {n} lines > {d['agents_max_lines']}: it is a map, move detail "
                         "into docs/PLAYBOOK.md or into a mechanical check")
    top = {p.name for p in root.iterdir()}
    md_files = [root / f for f in tracked_files(root) if f.endswith(".md") and (root / f).exists()]
    for f in md_files:
        rel = f.relative_to(root)
        text = f.read_text(encoding="utf-8")
        text_nocode = re.sub(r"```.*?```", "", text, flags=re.S)
        for target in LINK.findall(text_nocode):
            if re.match(r"^[a-z]+:|^#|^mailto:", target):
                continue
            path = target.split("#")[0]
            if path and not (f.parent / path).exists():
                probs.append(f"{rel}: broken link `{target}`")
        if rel.name in ("AGENTS.md", "FIXTURES.md", "INVARIANTS.md"):
            for tok in BACKTICK.findall(text_nocode):
                if "/" not in tok or re.search(r"[<>⟨⟩*{}$:|]", tok) or tok.startswith(("http", "-", ".")):
                    continue
                path = tok.split("#")[0].rstrip("/")
                if path.split("/")[0] in top and not (root / path).exists():
                    probs.append(f"{rel}: `{tok}` does not exist (stale path in a map file)")
    playbook = root / "docs" / "PLAYBOOK.md"
    if playbook.exists():
        rules = len(re.findall(r"^- \*\*", playbook.read_text(encoding="utf-8"), re.M))
        if rules > int(d["playbook_max_rules"]):
            probs.append(f"docs/PLAYBOOK.md has {rules} rules > {d['playbook_max_rules']}: merge them, or turn "
                         "the checkable ones into keel/boundary checks and delete the prose")
    plan = root / "PLAN.md"
    if plan.exists():
        nums = [int(x) for x in re.findall(r"^\|\s*(\d+)\s*\|", plan.read_text(encoding="utf-8"), re.M)]
        if nums != sorted(set(nums)):
            probs.append(f"PLAN.md decision numbers are not unique and increasing: {nums}")
    probs += check_registry(root, root / "docs" / "INVARIANTS.md", "INV")
    probs += check_registry(root, root / "docs" / "FIXTURES.md", None)
    progress = root / "docs" / "PROGRESS.md"
    if progress.exists():
        dates = re.findall(rf"^## ({DATE})", progress.read_text(encoding="utf-8"), re.M)
        if dates != sorted(dates, reverse=True):
            probs.append("docs/PROGRESS.md: entries must be newest first")
    probs += check_glossary(root, d.get("glossary_source_dirs", []))
    print(f"check-docs: {len(md_files)} markdown file(s), {len(probs)} problem(s)")
    if probs:
        raise KeelError("\n".join(probs))


def table_rows(text: str) -> list[list[str]]:
    rows = []
    for line in text.splitlines():
        if line.startswith("|") and not re.match(r"^\|[\s:|-]+\|$", line):
            rows.append([c.strip() for c in line.strip().strip("|").split("|")])
    return rows


def symbol_exists(root: Path, files: list[str], symbol: str) -> bool:
    for f in files:
        if f.endswith(".md"):
            continue
        try:
            if symbol in (root / f).read_text(encoding="utf-8", errors="ignore"):
                return True
        except (IsADirectoryError, FileNotFoundError):
            continue
    return False


def check_registry(root: Path, path: Path, prefix: str | None) -> list[str]:
    """Registries (invariants, fixtures) point at code. A pointer to nothing is a
    claim with no checker behind it, so every location must resolve."""
    if not path.exists():
        return []
    rel, rows = path.relative_to(root), table_rows(path.read_text(encoding="utf-8"))
    if len(rows) < 2:
        return []
    header, probs, seen = rows[0], [], set()
    loc_col = next((i for i, h in enumerate(header) if "位置" in h), None)
    files = tracked_files(root)
    for row in rows[1:]:
        if not row or not row[0] or PLACEHOLDER in "".join(row):
            continue
        key = row[0]
        if prefix and not re.match(rf"^{prefix}-\d+$", key):
            probs.append(f"{rel}: id `{key}` must look like {prefix}-<n>")
        if key in seen:
            probs.append(f"{rel}: `{key}` is listed twice")
        seen.add(key)
        if "计划" in " ".join(row) or loc_col is None or loc_col >= len(row):
            continue
        for tok in BACKTICK.findall(row[loc_col]):
            target = tok.split("::")[0].split("#")[0]
            if "/" in target or re.search(r"\.\w+$", target):
                if not (root / target).exists():
                    probs.append(f"{rel}: `{key}` points at missing path `{tok}`")
            elif not symbol_exists(root, files, tok):
                probs.append(f"{rel}: `{key}` points at `{tok}`, which appears in no source file")
    return probs


def check_glossary(root: Path, dirs: list[str]) -> list[str]:
    ctx = root / "CONTEXT.md"
    if not dirs or not ctx.exists():
        return []
    rows = table_rows(ctx.read_text(encoding="utf-8"))
    if not rows:
        return []
    col = next((i for i, h in enumerate(rows[0]) if "避免" in h), None)
    if col is None:
        return []
    terms = {t for r in rows[1:] if col < len(r) and PLACEHOLDER not in "".join(r)
             for t in BACKTICK.findall(r[col])}
    probs = []
    for f in tracked_files(root):
        if not any(f == d or f.startswith(d.rstrip("/") + "/") for d in dirs):
            continue
        try:
            lines = (root / f).read_text(encoding="utf-8", errors="ignore").splitlines()
        except (IsADirectoryError, FileNotFoundError):
            continue
        for i, line in enumerate(lines, 1):
            for term in terms:
                if re.search(rf"(?<![\w]){re.escape(term)}(?![\w])", line):
                    probs.append(f"{f}:{i}: uses `{term}`, which CONTEXT.md lists under 避免")
    return probs


def cmd_check_boundaries(root: Path, cfg: dict, args) -> None:
    rules = cfg["boundary"].get("rule", [])
    files = tracked_files(root)
    probs = []
    for rule in rules:
        targets = [f for f in files if glob_match(f, rule.get("paths", []))
                   and not glob_match(f, rule.get("except", []))]
        for f in targets:
            try:
                lines = (root / f).read_text(encoding="utf-8", errors="ignore").splitlines()
            except (IsADirectoryError, FileNotFoundError):
                continue
            for i, line in enumerate(lines, 1):
                for pat in rule.get("forbid", []):
                    if re.search(pat, line):
                        probs.append(f"{f}:{i}: [{rule.get('name', 'rule')}] matches `{pat}`")
    print(f"check-boundaries: {len(rules)} rule(s), {len(probs)} violation(s)")
    if probs:
        raise KeelError("\n".join(probs))


def cmd_snapshot(root: Path, cfg: dict, args) -> None:
    """Commit HEAD plus the given working-tree paths (normally just the new
    tests) as a private RED snapshot, without touching the index or branch."""
    head = resolve_commit(root, "HEAD")
    if not head:
        raise KeelError("snapshot needs at least one commit")
    fd, idx = tempfile.mkstemp(prefix="keel-index-")
    os.close(fd)
    os.unlink(idx)
    env = dict(os.environ, GIT_INDEX_FILE=idx)
    try:
        for step in (["read-tree", head], ["add", "--", *args.paths]):
            p = git(root, *step, env=env)
            if p.returncode != 0:
                raise KeelError(f"git {step[0]} failed: {p.stderr.strip()}")
        tree = git(root, "write-tree", env=env).stdout.strip()
    finally:
        if os.path.exists(idx):
            os.unlink(idx)
    msg = f"keel RED snapshot: HEAD + {' '.join(args.paths)}"
    p = git(root, "commit-tree", tree, "-p", head, "-m", msg)
    if p.returncode != 0:
        raise KeelError(f"commit-tree failed: {p.stderr.strip()}")
    sha = p.stdout.strip()
    retain(root, sha)
    print(sha)


def cmd_status(root: Path, cfg: dict, args) -> None:
    rows = []
    for path in card_paths(root, cfg):
        c = load_card(root, path)
        rows.append((c.meta.get("status", "?"), c.meta.get("type", "?"), c.meta.get("weight", "?"),
                     c.meta.get("created", "?"), Path(c.rel).name))
    order = {s: i for i, s in enumerate(("doing", "blocked", "awaiting-author", "todo", "done"))}
    rows.sort(key=lambda r: (order.get(r[0], 9), r[3]))
    if not args.all:
        rows = [r for r in rows if r[0] != "done"]
    for r in rows:
        print(f"{r[0]:<16} {r[1]:<9} {r[2]:<6} {r[3]:<11} {r[4]}")
    print(f"{len(rows)} card(s){'' if args.all else ' (done hidden; --all to show)'}")


def cmd_new(root: Path, cfg: dict, args) -> None:
    if args.type not in CARD_TYPES:
        raise KeelError(f"type must be one of {', '.join(CARD_TYPES)}")
    if not re.match(r"^[a-z0-9]+(?:-[a-z0-9]+)*$", args.slug):
        raise KeelError("slug must be lowercase kebab-case")
    tasks = root / cfg["paths"]["tasks"]
    template = tasks / ("TEMPLATE-LIGHT.md" if args.light else "TEMPLATE.md")
    dest = tasks / f"{args.type}-{args.slug}.md"
    if dest.exists():
        raise KeelError(f"{dest.relative_to(root)} already exists")
    text = template.read_text(encoding="utf-8")
    base = git(root, "rev-parse", "--short=12", "HEAD").stdout.strip() or "unborn"
    text = re.sub(r"^type:.*$", f"type: {args.type}", text, count=1, flags=re.M)
    text = re.sub(r"^created:.*$", f"created: {_dt.date.today()}", text, count=1, flags=re.M)
    text = re.sub(r"^baseline:.*$", f"baseline: {base}", text, count=1, flags=re.M)
    dest.write_text(text, encoding="utf-8")
    print(dest.relative_to(root))


def cmd_mutate(root: Path, cfg: dict, args) -> None:
    template = cfg["mutate"].get("command", [])
    if not template:
        raise KeelError("mutate.command is empty in keel.toml (see tools/presets/ for your stack)")
    mb = merge_base(root, args.base or cfg["guard"]["base"])
    diff_file = root / ".keel" / "cache" / "mutate.diff"
    diff_file.parent.mkdir(parents=True, exist_ok=True)
    diff_file.write_text(git(root, "diff", "--no-color", mb).stdout, encoding="utf-8")
    cmd = [a.replace("{base}", mb).replace("{diff}", str(diff_file)) for a in template]
    print("mutate:", " ".join(cmd), flush=True)
    code = subprocess.run(cmd, cwd=root).returncode
    if code != 0:
        raise KeelError(f"mutation run exited {code}: surviving mutants are tests that cannot tell the difference")


def cmd_init(root: Path, cfg: dict, args) -> None:
    preset = root / "tools" / "presets" / f"{args.stack}.toml"
    if not preset.exists():
        names = sorted(p.stem for p in (root / "tools" / "presets").glob("*.toml"))
        raise KeelError(f"no preset `{args.stack}`; available: {', '.join(names)}")
    text = preset.read_text(encoding="utf-8").replace("{{PROJECT_NAME}}", args.name)
    (root / "keel.toml").write_text(text, encoding="utf-8")
    for name in ("AGENTS.md", "PLAN.md", "CONTEXT.md", "README.project.md"):
        p = root / name
        if p.exists():
            p.write_text(p.read_text(encoding="utf-8").replace("{{PROJECT_NAME}}", args.name), encoding="utf-8")
    stub = root / "README.project.md"
    if stub.exists():
        shutil.move(stub, root / "README.md")
        # The other-language README describes the template, not the new project.
        (root / "README.zh-CN.md").unlink(missing_ok=True)
    progress = root / "docs" / "PROGRESS.md"
    if progress.exists():
        lines = progress.read_text(encoding="utf-8").split("\n## ")[0]
        progress.write_text(f"{lines.rstrip()}\n\n## {_dt.date.today()}\n\n- 做了什么：从 keel 模板初始化（stack={args.stack}）。\n"
                            "- 卡在哪：无。\n- 下一步：填 PLAN.md 的目标与非目标，开第一张卡。\n", encoding="utf-8")
    if (root / ".git").exists():
        git(root, "config", "core.hooksPath", ".githooks")
    print(f"initialized `{args.name}` with preset `{args.stack}`. Next: fill ⟨…⟩ in AGENTS.md / PLAN.md / CONTEXT.md")


# --------------------------------------------------------------------- main

def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(prog="keel", description="the workspace's trusted kernel (docs/KEEL.md)")
    ap.add_argument("--root", help="repository root (default: git toplevel)")
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("verify-red", help="re-run every RED claim and injected fault of a card")
    s.add_argument("card")
    s.add_argument("--at", help="revision injected faults apply to (default HEAD)")
    s.add_argument("--no-green", action="store_true", help="skip the post-fix green run")
    s = sub.add_parser("audit-red", help="check every RED commit still exists and is retained")
    s.add_argument("--retain", action="store_true")
    s = sub.add_parser("lint", help="structural lint of task cards")
    s.add_argument("cards", nargs="*")
    s = sub.add_parser("freeze-check", help="done definitions must not change after the freeze point")
    s.add_argument("cards", nargs="*")
    s = sub.add_parser("assert-guard", help="flag weakened tests in the diff")
    s.add_argument("--base")
    s = sub.add_parser("gate", help="run the configured gate and print a receipt")
    s.add_argument("--record", metavar="CARD", help="append the receipt to the card's 证据 section")
    s.add_argument("--keep-going", action="store_true")
    s.add_argument("--only", nargs="*")
    sub.add_parser("check-docs", help="map size, links, registries, decision numbers, glossary")
    sub.add_parser("check-boundaries", help="regex boundary rules from keel.toml")
    s = sub.add_parser("snapshot", help="commit HEAD + given paths as a retained RED snapshot")
    s.add_argument("paths", nargs="+")
    s = sub.add_parser("status", help="list cards by status")
    s.add_argument("--all", action="store_true")
    s = sub.add_parser("new", help="create a card from the template")
    s.add_argument("type")
    s.add_argument("slug")
    s.add_argument("--light", action="store_true")
    s = sub.add_parser("mutate", help="run the configured diff-scoped mutation tool")
    s.add_argument("--base")
    s = sub.add_parser("init", help="turn the template into a project")
    s.add_argument("--name", required=True)
    s.add_argument("--stack", required=True)
    args = ap.parse_args(argv)
    root = find_root(args.root)
    handlers = {"verify-red": cmd_verify_red, "audit-red": cmd_audit_red, "lint": cmd_lint,
                "freeze-check": cmd_freeze_check, "assert-guard": cmd_assert_guard, "gate": cmd_gate,
                "check-docs": cmd_check_docs, "check-boundaries": cmd_check_boundaries,
                "snapshot": cmd_snapshot, "status": cmd_status, "new": cmd_new, "mutate": cmd_mutate,
                "init": cmd_init}
    try:
        handlers[args.cmd](root, load_config(root), args)
    except KeelError as e:
        print(f"FAIL  {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
