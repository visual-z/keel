# keel

**English** | [中文](README.zh-CN.md)

A language-agnostic workspace template, plus `keel`: a single static binary that checks what task cards claim.

keel treats each task card as a **claim to be proven**. The definition of done is the specification, the tests are the proof, and `keel` is the proof checker. keel re-derives every claim a card makes from git and from real exit codes, and never takes the card's word for it. Typical claims are "this test was red before the fix", "the definition of done was not relaxed" and "the gate passed".

## Why

Letting a coding agent report its own "done" is unreliable. The usual shortcuts are:

- deleting assertions or adding skips;
- exiting the process before the asserts run;
- loosening the acceptance criteria;
- writing a test that was never red.

People under deadline pressure take the same shortcuts. keel's answer is to **trust only the checker**: a claim that a machine cannot re-verify does not count.

Your build tools (cargo, go, bun, tsc, pytest) check the *code*. keel checks *claims about history*, which no language toolchain checks:

- "this test failed at commit X";
- "this criterion is unchanged since the card was opened";
- "this diff removed an assertion";
- "this receipt covers every gate step".

That layer does not depend on the project's language, so keel is written once, in Go, and shipped as one binary with no runtime dependencies. Each project pins its version, the same way `rust-toolchain.toml` pins rustc.

## Install

```bash
mise use github:visual-z/keel@1.0.1                 # recommended: pins it in mise.toml
go install github.com/visual-z/keel/cmd/keel@v1.0.1 # with a Go toolchain
```

You can also download an archive from [Releases](https://github.com/visual-z/keel/releases) and verify it against `checksums.txt`.

Every repository pins the release it is checked with in `keel.toml` (`[keel] version = "1.0.1"`). A different keel refuses to run, so a result never depends on which keel happens to be on `PATH`.

## Start a project

```bash
mkdir demo && cd demo
keel init --name demo --stack cargo       # presets: cargo, go, bun, vitest, jest, pytest
keel new feat skeleton
```

For an existing project, run `keel adopt --stack <preset>`. It adds only the missing pieces (`keel.toml`, the card templates and the hooks) and freezes the cards that predate keel into a legacy list. That list may only shrink, and every new card must comply.

## What keel checks

| Command | The claim it verifies |
|---|---|
| `verify-red <card>` | *"This test was red before the fix."* keel re-runs the test in a throwaway worktree at the RED commit, where it must fail, and then in the working tree, where it must pass. With `repeat = N` every run must agree. Compile errors, load errors, skipped or zero selected tests, timeouts, crashes, and exit codes that disagree with the report do not count as red. |
| `verify-red` (fault patch) | *"The tests really catch this bug."* keel applies the card's `keel-inject` patch to HEAD and requires the tests to go red. The patch may touch product code only, never tests. A patch written before a file moved is applied where the file is now. |
| `freeze-check` | *"The definition of done was not relaxed."* keel compares the card with the commit that opened it. Criteria may only be added; changing one needs an author-confirmed revision. |
| `assert-guard` | *"No test was weakened."* keel scans the diff for removed assertions, new skip/only markers, early exits and `__eq__` overrides in tests, and deleted test files. A moved test is compared with its old content, so a layout change is clean; moving a test out of the test paths counts as deleting it. |
| `gate --record <card>` | *"Every check passed."* keel reads each step's exit code directly and counts the tests that actually ran (zero ran means failure). It writes a one-line receipt into the card; a partial run cannot be recorded. |
| `audit-red` | Every claim, in legacy cards too, names a commit that is retained under `refs/keel/red/`. |
| `lint`, `check-docs`, `check-boundaries` | Card structure and evidence; doc budgets, links, stale map paths, decision numbering, registries, glossary; regex architecture boundaries. |

keel reads test results from **output formats, not tools**. It prefers structured output (`go test -json`, JUnit XML) and parses text only where no stable structured format exists (cargo, bun). Full manual: [template/docs/KEEL.md](template/docs/KEEL.md) (in Chinese, like the rest of the process docs).

## Prior art and what was taken from it

| Project | What keel takes |
|---|---|
| [lefthook](https://github.com/evilmartians/lefthook), [prek](https://github.com/j178/prek) | A cross-language developer tool is one static binary with no runtime. prek exists because pre-commit's Python dependency was a burden, which is the same reason keel left Python. |
| [mise](https://mise.jdx.dev/dev-tools/backends/github.html) / [aqua](https://aquaproj.github.io/docs/) | Each repository pins tool versions; tools install from GitHub Releases named `<name>_<version>_<os>_<arch>.tar.gz`. |
| [regression-witness](https://github.com/qjf1597300-cloud/regression-witness) | Judge before/after runs from JUnit XML rather than text. The exit code must agree with the report, and repeated runs must agree. Missing, skipped and errored tests are never evidence. |
| SWE-bench FAIL_TO_PASS | A fix is evidenced by named tests that fail before it and pass after it. |
| [testscript](https://github.com/rogpeppe/go-internal) (go-internal) | keel's end-to-end tests are scripted scenario repositories, the way the Go team tests the `go` command. |
| xeno's `verify-red.ts` | The original: RED claims re-run in a throwaway worktree, snapshots retained under refs, strict bun summaries. Its tests were ported as keel scenarios. |

The design principles behind the card format are in [template/docs/PRINCIPLES.md](template/docs/PRINCIPLES.md). They draw on lessons from formal verification at scale, long-running agent harnesses and research on reward hacking.

## Layout

```
cmd/keel/            the binary
internal/keel/       the checks; testdata/script/*.txtar are end-to-end scenarios
internal/selfmut/    breaks each check in turn and requires the tests to notice
template/            what `keel init` writes (embedded in the binary)
  presets/           keel.toml per stack
scripts/release.sh   cross-compile, checksum and publish a release (no CI)
keel.toml            keel checks its own repository
```

## Developing keel

```bash
go test -race ./...           # unit tests + scenario repositories
go run ./internal/selfmut     # every check must have a test that catches its removal
keel gate                     # the repository's own gate
scripts/release.sh 1.0.1 --publish
```

When you add or change a check, add a scenario that catches the violation and one that passes clean input. Then add a mutant to `internal/selfmut`.
