package keel

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var patchPath = regexp.MustCompile(`(?m)^(?:\+\+\+ b|--- a)/(\S+)`)

func PatchPaths(patch string) []string {
	set := map[string]bool{}
	for _, m := range patchPath.FindAllStringSubmatch(patch, -1) {
		set[m[1]] = true
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// RenamesUpTo maps every path ever renamed in the history of rev to where it
// lives at rev, following chains (a → b → c gives a → c and b → c).
func RenamesUpTo(root, rev string) map[string]string {
	out, _ := Git(root, "-c", "core.quotePath=false", "log", "--find-renames", "--diff-filter=R", "--name-status", "--format=", "--reverse", rev)
	where := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 || !strings.HasPrefix(f[0], "R") {
			continue
		}
		for k, v := range where {
			if v == f[1] {
				where[k] = f[2]
			}
		}
		where[f[1]] = f[2]
	}
	return where
}

// renamePatchPath points a unified diff's headers for one file at its new path.
func renamePatchPath(patch, from, to string) string {
	var b strings.Builder
	for _, line := range strings.SplitAfter(patch, "\n") {
		switch {
		case line == "--- a/"+from+"\n" || line == "--- a/"+from:
			line = strings.Replace(line, from, to, 1)
		case line == "+++ b/"+from+"\n" || line == "+++ b/"+from:
			line = strings.Replace(line, from, to, 1)
		case strings.HasPrefix(line, "diff --git a/"+from+" b/"+from):
			line = strings.Replace(strings.Replace(line, "a/"+from, "a/"+to, 1), "b/"+from, "b/"+to, 1)
		}
		b.WriteString(line)
	}
	return b.String()
}

type verifyOpts struct {
	at      string
	noGreen bool
	repeat  int
}

// VerifyRed re-derives every RED claim and injected fault of one card:
// red at the claimed commit (or under the fault), green in the working tree.
// Repeated runs must agree, so a flaky test cannot certify anything.
func VerifyRed(root string, cfg *Config, cardPath string, o verifyOpts, w io.Writer) error {
	card, err := LoadCard(root, cardPath)
	if err != nil {
		return err
	}
	if len(card.Errors) > 0 {
		return failf("%s", strings.Join(card.Errors, "; "))
	}
	if len(card.Reds) == 0 && len(card.Injects) == 0 {
		return failf("%s has no `RED: <commit> <filter>` line and no keel-inject block", card.Rel)
	}
	type sel struct{ profile, filter string }
	var filters []sel
	type pin struct{ prefix, sha string }
	var pins []pin
	repeats := func(red RedConfig) int {
		if o.repeat > 0 {
			return o.repeat
		}
		return red.Repeat
	}
	for _, c := range card.Reds {
		sha, ok := ResolveCommit(root, c.Revision)
		if !ok {
			return failf("line %d: RED commit %s does not exist in this repository", c.Line, c.Revision)
		}
		red, err := cfg.RedFor(c.Profile)
		if err != nil {
			return err
		}
		label := ""
		if c.Profile != "" {
			label = "(" + c.Profile + ") "
		}
		fmt.Fprintf(w, "RED     %s  %s%s\n", sha[:12], label, c.Filter)
		var last RunOutcome
		for n := 0; n < repeats(red); n++ {
			wt, err := NewWorktree(root, sha, red.Link)
			if err != nil {
				return err
			}
			out, err := RunFilter(root, red, wt.Path, c.Filter)
			wt.Close()
			if err != nil {
				return err
			}
			if err := RequireRed(red, out, fmt.Sprintf("line %d (run %d)", c.Line, n+1)); err != nil {
				return err
			}
			last = out
		}
		pins = append(pins, pin{red.RefPrefix, sha})
		fmt.Fprintf(w, "        red: %d/%d selected tests failed, exit %d (%d run(s))\n", last.Sum.Failed, last.Sum.Selected, last.Res.Code, repeats(red))
		filters = append(filters, sel{c.Profile, c.Filter})
	}
	if len(card.Injects) > 0 {
		at := o.at
		if at == "" {
			at = "HEAD"
		}
		base, ok := ResolveCommit(root, at)
		if !ok {
			return failf("cannot resolve %s", at)
		}
		var renames map[string]string
		for _, inj := range card.Injects {
			touched := PatchPaths(inj.Patch)
			if len(touched) == 0 {
				return failf("line %d: keel-inject block is not a unified diff", inj.Line)
			}
			// A fault written before a move names the file where it was. It is
			// applied where that file is now, and checked against tests there.
			var moved []string
			for i, p := range touched {
				if _, exists := Git(root, "cat-file", "-e", base+":"+p); exists {
					continue
				}
				if renames == nil {
					renames = RenamesUpTo(root, base)
				}
				if to, ok := renames[p]; ok {
					inj.Patch = renamePatchPath(inj.Patch, p, to)
					touched[i] = to
					moved = append(moved, to+", renamed from "+p)
				}
			}
			var bad []string
			for _, p := range touched {
				if GlobMatch(p, cfg.Tests.Paths) {
					bad = append(bad, p)
				}
			}
			if len(bad) > 0 {
				return failf("line %d: an injected fault must break product code, not tests: %s", inj.Line, strings.Join(bad, ", "))
			}
			red, err := cfg.RedFor(inj.Profile)
			if err != nil {
				return err
			}
			shown := touched
			if len(moved) > 0 {
				shown = moved
			}
			fmt.Fprintf(w, "INJECT  %s+patch  %s  (%s)\n", base[:12], inj.Filter, strings.Join(shown, ", "))
			var last RunOutcome
			for n := 0; n < repeats(red); n++ {
				wt, err := NewWorktree(root, base, red.Link)
				if err != nil {
					return err
				}
				// --unidiff-zero: a one-line fault is naturally written without context.
				if _, msg, ok := GitIn(wt.Path, inj.Patch, nil, "apply", "--unidiff-zero", "--whitespace=nowarn", "-"); !ok {
					wt.Close()
					return failf("line %d: the fault patch does not apply to %s: %s", inj.Line, base[:12], msg)
				}
				out, err := RunFilter(root, red, wt.Path, inj.Filter)
				wt.Close()
				if err != nil {
					return err
				}
				if err := RequireRed(red, out, fmt.Sprintf("line %d (run %d)", inj.Line, n+1)); err != nil {
					return err
				}
				last = out
			}
			fmt.Fprintf(w, "        red: %d/%d selected tests failed under the injected fault\n", last.Sum.Failed, last.Sum.Selected)
			filters = append(filters, sel{inj.Profile, inj.Filter})
		}
	}
	if o.noGreen {
		fmt.Fprintln(w, "GREEN   skipped (--no-green)")
	} else {
		seen := map[sel]bool{}
		for _, f := range filters {
			if seen[f] {
				continue
			}
			seen[f] = true
			red, _ := cfg.RedFor(f.profile)
			for n := 0; n < repeats(red); n++ {
				out, err := RunFilter(root, red, root, f.filter)
				if err != nil {
					return err
				}
				if err := RequireGreen(out, fmt.Sprintf("green `%s` (run %d)", f.filter, n+1)); err != nil {
					return err
				}
				if n == repeats(red)-1 {
					fmt.Fprintf(w, "GREEN   working tree  %s  (%d selected, all pass)\n", f.filter, out.Sum.Selected)
				}
			}
		}
	}
	// Retained only after the whole card verified (every red and every green):
	// a refused verification leaves no ref behind.
	for _, p := range pins {
		if RefStateOf(root, p.prefix, p.sha) == RefConflict {
			return failf("%s%s exists and points elsewhere; refusing to move it", p.prefix, p.sha)
		}
	}
	for _, p := range pins {
		if err := Retain(root, p.prefix, p.sha); err != nil {
			return err
		}
	}
	fmt.Fprintf(w, "ok      %d RED claim(s), %d injected fault(s) verified\n", len(card.Reds), len(card.Injects))
	return nil
}

type auditReport struct {
	Version              int      `json:"version"`
	Mode                 string   `json:"mode"`
	Verification         string   `json:"verification"`
	Claims               int      `json:"claims"`
	TemplateDeclarations int      `json:"templateDeclarations"`
	Commits              int      `json:"commits"`
	Retained             int      `json:"retained"`
	Unretained           int      `json:"unretained"`
	MissingClaims        int      `json:"missingClaims"`
	InvalidCards         int      `json:"invalidCards"`
	Conflicts            []string `json:"conflicts"`
	Problems             []string `json:"problems"`
}

// AuditRed checks every claim in every card (legacy ones included: a claim
// is a claim whatever the card's format) still names a retained commit. It
// does not re-run tests. --retain validates the whole set before retaining
// anything, so a bad claim never leaves the others half retained.
func AuditRed(root string, cfg *Config, retain, asJSON bool, w io.Writer) error {
	rep := auditReport{Version: 1, Mode: "audit", Verification: "not_run", Conflicts: []string{}, Problems: []string{}}
	if retain {
		rep.Mode = "retain"
	}
	prefix := cfg.Red.RefPrefix
	shas := map[string]bool{}
	tdir := filepath.Join(root, cfg.Paths.Tasks)
	if entries, err := os.ReadDir(tdir); err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "TEMPLATE") && strings.HasSuffix(e.Name(), ".md") {
				for _, l := range lines(readFile(filepath.Join(tdir, e.Name()))) {
					if redPrefix.MatchString(l) {
						rep.TemplateDeclarations++
					}
				}
			}
		}
	}
	for _, path := range AllCardPaths(root, cfg) {
		card, err := LoadCard(root, path)
		if err != nil {
			return err
		}
		if len(card.RedErrors()) > 0 {
			rep.InvalidCards++
			rep.Claims += len(card.RedErrors())
			for _, e := range card.RedErrors() {
				rep.Problems = append(rep.Problems, card.Rel+": "+e)
			}
		}
		for _, c := range card.Reds {
			rep.Claims++
			sha, ok := ResolveCommit(root, c.Revision)
			if !ok {
				rep.MissingClaims++
				rep.Problems = append(rep.Problems, fmt.Sprintf("%s:%d: commit %s is missing", card.Rel, c.Line, c.Revision))
				continue
			}
			shas[sha] = true
		}
	}
	sorted := make([]string, 0, len(shas))
	for s := range shas {
		sorted = append(sorted, s)
	}
	sort.Strings(sorted)
	rep.Commits = len(sorted)
	for _, s := range sorted {
		if RefStateOf(root, prefix, s) == RefConflict {
			rep.Conflicts = append(rep.Conflicts, s)
			rep.Problems = append(rep.Problems, fmt.Sprintf("%s%s conflicts (exists elsewhere or is symbolic)", prefix, s))
		}
	}
	clean := rep.InvalidCards == 0 && rep.MissingClaims == 0 && len(rep.Conflicts) == 0
	if retain && clean {
		for _, s := range sorted {
			if err := Retain(root, prefix, s); err != nil {
				return err
			}
		}
	}
	for _, s := range sorted {
		switch RefStateOf(root, prefix, s) {
		case RefRetained:
			rep.Retained++
		case RefMissing:
			rep.Unretained++
		}
	}
	ok := clean && rep.Retained == rep.Commits
	if asJSON {
		b, _ := json.Marshal(rep)
		fmt.Fprintln(w, string(b))
	} else {
		for _, p := range rep.Problems {
			fmt.Fprintln(w, "      - "+p)
		}
		hint := ""
		if rep.Unretained > 0 && !retain {
			hint = " (run audit-red --retain)"
		}
		fmt.Fprintf(w, "audit-red: %d claim(s), %d commit(s), %d retained, %d unretained%s; tests not re-run (use verify-red)\n",
			rep.Claims, rep.Commits, rep.Retained, rep.Unretained, hint)
	}
	if !ok {
		return failf("audit-red failed")
	}
	return nil
}

// Snapshot commits HEAD plus the given working-tree paths (normally just the
// new tests) as a private RED snapshot, without touching index or branch.
func Snapshot(root string, cfg *Config, paths []string, w io.Writer) error {
	head, ok := ResolveCommit(root, "HEAD")
	if !ok {
		return failf("snapshot needs at least one commit")
	}
	f, err := os.CreateTemp("", "keel-index-")
	if err != nil {
		return err
	}
	idx := f.Name()
	f.Close()
	os.Remove(idx)
	defer os.Remove(idx)
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	if _, msg, ok := GitIn(root, "", env, "read-tree", head); !ok {
		return failf("git read-tree failed: %s", msg)
	}
	if _, msg, ok := GitIn(root, "", env, append([]string{"add", "--"}, paths...)...); !ok {
		return failf("git add failed: %s", msg)
	}
	tree, msg, ok := GitIn(root, "", env, "write-tree")
	if !ok {
		return failf("git write-tree failed: %s", msg)
	}
	sha, msg, ok := GitIn(root, "", nil, "commit-tree", tree, "-p", head, "-m", "keel RED snapshot: HEAD + "+strings.Join(paths, " "))
	if !ok {
		return failf("commit-tree failed: %s", msg)
	}
	if err := Retain(root, cfg.Red.RefPrefix, sha); err != nil {
		return err
	}
	fmt.Fprintln(w, sha)
	return nil
}
