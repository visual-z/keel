# keel

**English** | [中文](README.zh-CN.md)

A project-agnostic workspace template, meant to be the starting point for every new project.

keel treats each task card as a **claim to be proven**. The definition of done is the specification, the tests are the proof, and `tools/keel.py` is the proof checker. keel re-derives every claim a card makes from git and from real exit codes, and never takes the card's word for it. Typical claims are "this test was red before the fix", "the definition of done was not relaxed" and "the gate passed".

## Why

Letting a coding agent report its own "done" is unreliable. The usual shortcuts are:

- deleting assertions or adding skips;
- exiting the process before the asserts run;
- loosening the acceptance criteria;
- writing a test that was never red.

People under deadline pressure take the same shortcuts. keel's answer is to **trust only the checker**: a claim that a machine cannot re-verify does not count.

[docs/PRINCIPLES.md](docs/PRINCIPLES.md) explains the design. Each principle names its source and the mechanism that implements it. The sources are lessons from formal verification at scale, long-running agent harnesses, harness engineering, and research on reward hacking in coding agents. The principles document is written in Chinese, like the rest of the process docs. The code and its comments are in English.

## Start a new project

```bash
gh repo create <name> --private --template visual-z/keel --clone
cd <name>
python3 tools/keel.py init --name <name> --stack <cargo|go|bun|vitest|jest|pytest>
```

`init` does the following:

- writes `keel.toml` from `tools/presets/<stack>.toml`;
- replaces `{{PROJECT_NAME}}`;
- swaps this README for a project README;
- enables `.githooks`;
- adds an entry to `docs/PROGRESS.md`.

Next, fill in the `⟨…⟩` placeholders in `AGENTS.md`, `PLAN.md` and `CONTEXT.md`, then open your first card:

```bash
python3 tools/keel.py new feat skeleton
```

The only requirements are Python 3.11+ and git.

## What keel checks

| Command | The claim it verifies |
|---|---|
| `verify-red <card>` | *"This test was red before the fix."* keel re-runs the test in a throwaway worktree at the RED commit, where it must fail, and then in the working tree, where it must pass. Compile errors, load errors, filters that select zero tests, and timeouts do not count as red. |
| `verify-red` (fault patch) | *"The tests really catch this bug."* keel applies the card's `keel-inject` patch on top of HEAD, and the tests must go red. The patch may only touch product code, never tests. |
| `freeze-check` | *"The definition of done was not relaxed."* keel compares the card with the commit that opened it, which is the freeze point. Criteria may only be added. Changing one needs an author-confirmed entry under `## 修订` (revisions). |
| `assert-guard` | *"No test was weakened."* keel scans the diff for removed assertions, new skip/only markers, early exits and `__eq__` overrides in tests, and deleted test files. |
| `gate --record <card>` | *"Every check passed."* keel reads each step's exit code directly and parses the test counts. A test step that selects zero tests fails. keel then writes a one-line receipt into the card. |
| `lint` | Cards are well formed. Every criterion `D<n>` is bound to a check. A card that claims to be finished has its evidence, and its hand checks are confirmed by the author. |
| `check-docs` | `AGENTS.md` and the playbook stay within their size budgets. There are no broken links or stale paths. Decision numbers increase. Invariants and fixtures point at code that exists. Terms the glossary forbids do not appear in the source. |
| `check-boundaries` | The architecture boundaries declared in `keel.toml`, enforced as regex rules. |
| `mutate` | Mutation testing restricted to the code this branch changed. |

The full manual is [docs/KEEL.md](docs/KEEL.md).

## The life of a card

1. Run `keel new fix <slug>` and write the definition of done. Bind every criterion `D<n>` to a check.
2. **Opening commit:** commit the card together with the new, failing test. This commit is both the RED evidence and the freeze point of the definition of done.
3. Implement the change. The definition of done may only get stricter.
4. Run `keel verify-red`, `keel assert-guard` and `keel gate --record`.
5. Set the status to `done`. Changes the author can see stay at `awaiting-author` until the author has tried them by hand.

**There is no CI.** All evidence is produced locally, by the git hooks and the commands above. The pre-commit hook runs only the fast checks: lint, freeze, docs, boundaries and the test-weakening guard. Run the full gate and `verify-red` by hand before you mark a card done.

## Layout

```
AGENTS.md            single entry point for agents (a map, ≤150 lines); CLAUDE.md only imports it
PLAN.md              goals, non-goals, budgets, layered milestones, decision log
CONTEXT.md           glossary; terms listed under "avoid" fail the build if they appear in source
keel.toml            the only machine config: gate steps, how to run RED, test patterns, boundaries
docs/
  PRINCIPLES.md      why the process is shaped this way (principle → source → mechanism)
  PLAYBOOK.md        topic rules as "one sentence + criterion", with a size budget
  KEEL.md            keel commands and card format
  INVARIANTS.md      invariant registry (code assertion > property > model check > example)
  FIXTURES.md        reusable verification fixtures (the "lemmas")
  PROGRESS.md        session handoff, newest first
  tasks/             task cards and the two templates
  examples/          one fully filled-in card
  adr/  research/    templates for major decisions and research notes
tools/
  keel.py            the trusted kernel: one file, standard library only
  presets/           keel.toml presets per stack
  tests/             keel's own tests
  self_mutation.py   breaks each keel check in turn and requires the tests to notice
.githooks/           pre-commit and commit-msg (Conventional Commits)
.claude/settings.json  push and amend ask first; force push and `git add -A` are denied
```

## Improving the template

This repository checks itself with keel:

```bash
python3 tools/keel.py gate          # tests + every check
python3 tools/self_mutation.py      # removing any check must turn some test red
```

When you add or change a check, add both a "catches the violation" test and a "clean input passes" test to `tools/tests/test_keel.py`. Then add a matching mutant to `tools/self_mutation.py`.
