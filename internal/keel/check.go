package keel

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func Lint(root string, cfg *Config, cards []string, w io.Writer) error {
	paths := cards
	if len(paths) == 0 {
		paths = CardPaths(root, cfg)
	}
	failed := 0
	for _, p := range paths {
		card, err := LoadCard(root, p)
		if err != nil {
			return err
		}
		if probs := LintCard(card, cfg.StepNames()); len(probs) > 0 {
			failed++
			fmt.Fprintf(w, "FAIL  %s\n", card.Rel)
			for _, pr := range probs {
				fmt.Fprintf(w, "      - %s\n", pr)
			}
		}
	}
	legacy := LegacyNames(root, cfg)
	existing := map[string]bool{}
	for _, p := range AllCardPaths(root, cfg) {
		existing[filepath.Base(p)] = true
	}
	var stale []string
	for _, n := range legacy {
		if !existing[n] {
			stale = append(stale, n)
		}
	}
	if len(stale) > 0 && len(cards) == 0 {
		failed++
		more := ""
		if len(stale) > 5 {
			stale, more = stale[:5], " …"
		}
		fmt.Fprintf(w, "FAIL  %s: lists missing card(s): %s%s\n", cfg.Paths.LegacyManifest, strings.Join(stale, ", "), more)
	}
	note := ""
	if len(legacy) > 0 {
		note = fmt.Sprintf(", %d legacy skipped", len(legacy)-len(stale))
	}
	fmt.Fprintf(w, "lint: %d card(s), %d with problems%s\n", len(paths), failed, note)
	if failed > 0 {
		return failf("card lint failed")
	}
	return nil
}

// FreezeCheck compares every card's done-definition with the commit that
// opened it. Criteria may be added; changing or removing one needs an
// author-confirmed entry under `## 修订`.
// FreezeCheck: in the pre-commit hook (staged=true) a card staged in the
// commit being made is frozen by that commit, so the opening commit itself
// can pass; everywhere else a card must already be in history.
func FreezeCheck(root string, cfg *Config, cards []string, w io.Writer) error {
	return freezeCheck(root, cfg, cards, false, w)
}

func freezeCheck(root string, cfg *Config, cards []string, staged bool, w io.Writer) error {
	paths := cards
	if len(paths) == 0 {
		paths = CardPaths(root, cfg)
	}
	var probs []string
	if len(cards) == 0 {
		sha, text, ok, err := FirstCommitted(root, cfg.Paths.LegacyManifest)
		if err != nil {
			return err
		}
		if ok {
			was := map[string]bool{}
			for _, n := range ReadManifest(text) {
				was[n] = true
			}
			var grown []string
			for _, n := range LegacyNames(root, cfg) {
				if !was[n] {
					grown = append(grown, n)
				}
			}
			if len(grown) > 0 {
				sort.Strings(grown)
				if len(grown) > 5 {
					grown = grown[:5]
				}
				probs = append(probs, fmt.Sprintf("%s: grew since %s (%s); the legacy list may only shrink: new cards must comply",
					cfg.Paths.LegacyManifest, sha[:12], strings.Join(grown, ", ")))
			}
		}
	}
	for _, p := range paths {
		card, err := LoadCard(root, p)
		if err != nil {
			return err
		}
		status, weight := card.Meta["status"], card.Meta["weight"]
		sha, old, ok, err := FirstCommitted(root, card.Rel)
		if err != nil {
			return err
		}
		if !ok {
			if staged {
				if _, inIndex := Git(root, "cat-file", "-e", ":"+card.Rel); inIndex {
					continue // this commit is the freeze point
				}
			}
			if status != "" && status != "todo" {
				probs = append(probs, fmt.Sprintf("%s: status `%s` but the card was never committed; commit the card (the freeze point) before implementing", card.Rel, status))
			}
			continue
		}
		frozen := ParseCard(old, card.Rel)
		if frozen.Meta["status"] == "done" && weight == "full" {
			probs = append(probs, fmt.Sprintf("%s: first committed already `done` at %s; a full card needs a freeze point committed before the implementation", card.Rel, sha[:12]))
		}
		ids := make([]int, 0, len(frozen.Items))
		for id := range frozen.Items {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			if card.Revised[id] {
				continue
			}
			now, ok := card.Items[id]
			switch {
			case !ok:
				probs = append(probs, fmt.Sprintf("%s: D%d was removed after the freeze at %s without an author-confirmed entry in `## 修订`", card.Rel, id, sha[:12]))
			case now.FrozenText() != frozen.Items[id].FrozenText():
				probs = append(probs, fmt.Sprintf("%s: D%d changed after the freeze at %s without an author-confirmed entry in `## 修订`", card.Rel, id, sha[:12]))
			}
		}
	}
	fmt.Fprintf(w, "freeze-check: %d card(s), %d problem(s)\n", len(paths), len(probs))
	if len(probs) > 0 {
		return failf("%s", strings.Join(probs, "\n"))
	}
	return nil
}

type guardStats struct{ assertAdd, assertDel, skipAdd, suspAdd int }

func compileAll(pats []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, failf("bad pattern %q in keel.toml: %v", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func anyMatch(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// AssertGuard: agents most often "finish" by weakening tests — deleting
// assertions, adding skips, exiting before the asserts run, or overriding
// equality. Count them in the diff against the merge base.
func AssertGuard(root string, cfg *Config, base string, w io.Writer) error {
	t := cfg.Tests
	scope := append(append([]string{}, t.Paths...), t.InlinePaths...)
	if len(scope) == 0 {
		return failf("tests.paths is empty in keel.toml; the guard cannot know what a test is")
	}
	if _, ok := ResolveCommit(root, "HEAD"); !ok {
		fmt.Fprintln(w, "assert-guard: no commit yet, nothing to compare against")
		return nil
	}
	if base == "" {
		base = cfg.Guard.Base
	}
	mb, err := MergeBase(root, base)
	if err != nil {
		return err
	}
	asserts, err := compileAll(t.AssertPatterns)
	if err != nil {
		return err
	}
	skips, err := compileAll(t.SkipPatterns)
	if err != nil {
		return err
	}
	susp, err := compileAll(t.SuspiciousPatterns)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, p := range CardPaths(root, cfg) {
		if c, err := LoadCard(root, p); err == nil {
			for _, f := range c.WeakenOK {
				allowed[f] = true
			}
		}
	}
	// Renames are detected so a moved test is compared with its own old
	// content: moving a tree of tests (a repository becoming a monorepo) adds
	// nothing, and a weakening inside a moved file is still charged to it.
	// A move across the edge of the test paths is not a move for the guard:
	// out of them it hides a test as surely as deleting it, and into them
	// every line is new to the guard, its early exits too.
	r, err := Run(root, nil, 0, "", "git", "-c", "core.quotePath=false", "diff", "--no-color", "--unified=0", "--find-renames", mb, "--")
	if err != nil {
		return err
	}
	stats := map[string]*guardStats{}
	count := func(file, line string) {
		if file == "" || line == "" || (line[0] != '+' && line[0] != '-') || !GlobMatch(file, scope) {
			return
		}
		s := stats[file]
		if s == nil {
			s = &guardStats{}
			stats[file] = s
		}
		body := line[1:]
		if anyMatch(asserts, body) {
			if line[0] == '+' {
				s.assertAdd++
			} else {
				s.assertDel++
			}
		}
		if line[0] == '+' && anyMatch(skips, body) {
			s.skipAdd++
		}
		if line[0] == '+' && anyMatch(susp, body) {
			s.suspAdd++
		}
	}
	type move struct{ from, to string }
	var moves []move
	current, renameFrom, inHeader := "", "", false
	for _, line := range lines(r.Stdout) {
		// File headers only occur between `diff --git` and the first `@@`; a
		// removed line whose content starts with `--` is not one.
		if strings.HasPrefix(line, "diff --git ") {
			current, renameFrom, inHeader = "", "", true
			continue
		}
		if inHeader {
			switch {
			case strings.HasPrefix(line, "rename from "):
				renameFrom = strings.TrimPrefix(line, "rename from ")
			case strings.HasPrefix(line, "rename to "):
				moves = append(moves, move{renameFrom, strings.TrimPrefix(line, "rename to ")})
			case strings.HasPrefix(line, "--- a/"), strings.HasPrefix(line, "+++ b/"):
				current = line[6:]
			case strings.HasPrefix(line, "@@"):
				inHeader = false
			}
			continue
		}
		if strings.HasPrefix(line, "@@") {
			continue
		}
		// A move across the edge is judged below, from the whole file.
		if n := len(moves); n > 0 && moves[n-1].to == current && GlobMatch(moves[n-1].from, scope) != GlobMatch(current, scope) {
			continue
		}
		count(current, line)
	}
	deleted, _ := Git(root, "-c", "core.quotePath=false", "diff", "--name-only", "--diff-filter=D", "--find-renames", mb, "--")
	var probs []string
	for _, f := range strings.Fields(deleted) {
		if GlobMatch(f, t.Paths) && !allowed[f] {
			probs = append(probs, f+": test file deleted")
		}
	}
	for _, m := range moves {
		switch inFrom, inTo := GlobMatch(m.from, scope), GlobMatch(m.to, scope); {
		case inFrom && !inTo && GlobMatch(m.from, t.Paths) && !allowed[m.from]:
			probs = append(probs, fmt.Sprintf("%s: test file moved out of the test paths to %s", m.from, m.to))
		case !inFrom && inTo:
			whole, err := Run(root, nil, 0, "", "git", "diff", "--no-color", "--unified=0", "--no-renames", mb, "--", m.to)
			if err != nil {
				return err
			}
			for _, line := range lines(whole.Stdout) {
				if !strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "--- ") {
					count(m.to, line)
				}
			}
		}
	}
	files := make([]string, 0, len(stats))
	for f := range stats {
		files = append(files, f)
	}
	sort.Strings(files)
	for _, f := range files {
		if allowed[f] {
			continue
		}
		s := stats[f]
		if s.assertDel > s.assertAdd {
			probs = append(probs, fmt.Sprintf("%s: assertions dropped (%d removed, %d added)", f, s.assertDel, s.assertAdd))
		}
		if s.skipAdd > 0 {
			probs = append(probs, fmt.Sprintf("%s: %d skip/only marker(s) added", f, s.skipAdd))
		}
		if s.suspAdd > 0 {
			probs = append(probs, fmt.Sprintf("%s: %d early-exit or always-equal pattern(s) added", f, s.suspAdd))
		}
	}
	fmt.Fprintf(w, "assert-guard: diff against %s, %d test file(s) touched, %d problem(s)\n", mb[:12], len(stats), len(probs))
	if len(probs) > 0 {
		return failf("%s\n      If intended, the card must say `WEAKEN-OK: <path> <reason>（作者确认 YYYY-MM-DD）`", strings.Join(probs, "\n"))
	}
	return nil
}

func CheckBoundaries(root string, cfg *Config, w io.Writer) error {
	files := TrackedFiles(root)
	var probs []string
	for _, rule := range cfg.Boundary.Rule {
		forbid, err := compileAll(rule.Forbid)
		if err != nil {
			return err
		}
		for _, f := range files {
			if !GlobMatch(f, rule.Paths) || GlobMatch(f, rule.Except) {
				continue
			}
			for i, line := range lines(readFile(filepath.Join(root, f))) {
				for j, re := range forbid {
					if re.MatchString(line) {
						probs = append(probs, fmt.Sprintf("%s:%d: [%s] matches `%s`", f, i+1, rule.Name, rule.Forbid[j]))
					}
				}
			}
		}
	}
	fmt.Fprintf(w, "check-boundaries: %d rule(s), %d violation(s)\n", len(cfg.Boundary.Rule), len(probs))
	if len(probs) > 0 {
		return failf("%s", strings.Join(probs, "\n"))
	}
	return nil
}
