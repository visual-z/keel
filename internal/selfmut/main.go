// Command selfmut is keel's own mutation test: it breaks one check at a time
// in a copy of the source and requires the test suite to go red. A check
// whose removal no test notices is a check nothing verifies.
//
//	go run ./internal/selfmut
//
// Add a mutant here whenever you add or change a check in internal/keel.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type mutant struct{ name, file, old, new string }

var mutants = []mutant{
	// verify-red: what counts as red
	{"timeout accepted as red", "runner.go", "\tcase r.TimedOut:\n\t\treturn failf(\"%s: the test run timed out", "\tcase false:\n\t\treturn failf(\"%s: the test run timed out"},
	{"crash accepted as red", "runner.go", "\tcase r.Signaled:\n\t\treturn failf(\"%s: the runner was killed", "\tcase false:\n\t\treturn failf(\"%s: the runner was killed"},
	{"invalid output accepted", "runner.go", "\tcase s.Invalid != \"\":\n\t\treturn failf(\"%s: %s\\n%s\", where, s.Invalid", "\tcase false:\n\t\treturn failf(\"%s: %s\\n%s\", where, s.Invalid"},
	{"zero selected accepted", "runner.go", "\tcase s.Selected == 0:\n\t\treturn failf(\"%s: the filter selected 0 tests; a missing", "\tcase false:\n\t\treturn failf(\"%s: the filter selected 0 tests; a missing"},
	{"passing test accepted", "runner.go", "\tcase r.Code == 0:\n\t\treturn failf(\"%s: the test PASSED", "\tcase false:\n\t\treturn failf(\"%s: the test PASSED"},
	{"wrong exit code accepted", "runner.go", "\tcase !redExitOK(red, r.Code):", "\tcase false:"},
	{"green check skipped", "runner.go", "\tif r.Code != 0 || r.TimedOut || r.Signaled || s.Invalid != \"\" || s.Failed > 0 {", "\tif false {"},
	// parsers
	{"cargo test failure read as compile error", "runner.go", "`(?m)^error\\[E\\d+\\]:|^error: could not compile|^error: linking with`", "`(?m)^error(\\[E\\d+\\])?:|could not compile`"},
	{"cargo counts ignored as run", "runner.go", "\t\ts.Selected += p + f\n", "\t\ts.Selected += p + f + i\n"},
	{"bun loader errors accepted", "runner.go", "\tif bunErrors.MatchString(out) || strings.Contains(out, \"Unhandled error between tests\") {", "\tif false {"},
	{"bun duplicate summaries accepted", "runner.go", "\tif len(pass) != 1 || len(fail) != 1 || !bunRan.MatchString(out) {", "\tif false {"},
	{"go build failure accepted", "runner.go", "\t\tcase e.Action == \"build-fail\":", "\t\tcase false:"},
	{"go package failure accepted", "runner.go", "\t\tif !testFailIn[pkg] {", "\t\tif false {"},
	{"go skip counted as run", "runner.go", "\t\tcase e.Test != \"\" && e.Action == \"skip\":\n\t\t\ts.Skipped++", "\t\tcase e.Test != \"\" && e.Action == \"skip\":\n\t\t\ts.Selected++"},
	{"junit error accepted", "runner.go", "\t\t\tcase len(c.Error) > 0:", "\t\t\tcase false:"},
	{"junit duplicate accepted", "runner.go", "\t\t\tif seen[id] {", "\t\t\tif false {"},
	{"junit missing report accepted", "runner.go", "\t\treturn Summary{Invalid: \"no JUnit report was written", "\t\treturn Summary{Selected: 1, Failed: 1}\n\t\t_ = Summary{Invalid: \"no JUnit report was written"},
	// verify-red mechanics
	{"inject may edit tests", "verify.go", "\t\t\tif len(bad) > 0 {", "\t\t\tif false {"},
	{"zero-context patch refused", "verify.go", "\"apply\", \"--unidiff-zero\", ", "\"apply\", "},
	{"retained before green", "verify.go", "\t\tpins = append(pins, pin{red.RefPrefix, sha})\n", "\t\tpins = append(pins, pin{red.RefPrefix, sha})\n\t\t_ = Retain(root, red.RefPrefix, sha)\n"},
	{"green shares the fault cache", "runner.go", "\tisolated := filepath.Clean(tree) != filepath.Clean(root)", "\tisolated := true"},
	{"repeat ignored", "verify.go", "\tif o.repeat > 0 {\n\t\t\treturn o.repeat", "\tif false {\n\t\t\treturn o.repeat"},
	{"profile ignored", "config.go", "\tp, ok := c.Red.Profile[profile]", "\tp, ok := c.Red, true"},
	{"args not split", "runner.go", "\t\t\tparts, err := ShellSplit(filter)", "\t\t\tparts, err := []string{filter}, error(nil)"},
	// retention
	{"conflicting ref moved", "git.go", "\tcase RefConflict:\n\t\treturn failf(\"%s%s exists and points elsewhere; refusing", "\tcase RefConflict:\n\t\tGitIn(root, \"\", nil, \"update-ref\", prefix+sha, sha)\n\t\treturn nil\n\t\t_ = failf(\"%s%s exists and points elsewhere; refusing"},
	{"symbolic ref not a conflict", "git.go", "\tif _, ok := Git(root, \"symbolic-ref\", \"--quiet\", ref); ok {", "\tif false {"},
	{"audit retains partially", "verify.go", "\tif retain && clean {", "\tif retain {"},
	// cards
	{"malformed claim ignored", "card.go", "\t\tif redPrefix.MatchString(line) {", "\t\tif redLine.MatchString(line) && hexRev.MatchString(redLine.FindStringSubmatch(line)[2]) {"},
	{"comment claims parsed", "card.go", "\t\t\tif inComment {\n\t\t\t\tinComment = !strings.Contains(line, \"-->\")\n\t\t\t\tcontinue", "\t\t\tif false && inComment {\n\t\t\t\tinComment = !strings.Contains(line, \"-->\")\n\t\t\t\tcontinue"},
	{"unchecked criterion ok", "card.go", "\t\tif len(it.Checks) == 0 {", "\t\tif false {"},
	{"placeholder lint off", "card.go", "\tif started && strings.Contains(c.Text, Placeholder) {", "\tif false && started {"},
	{"done without receipt", "card.go", "\t\tif len(oks) == 0 {", "\t\tif false {"},
	{"partial receipt accepted", "card.go", "\t\t\tif !covered {", "\t\t\tif false && !covered {"},
	{"receipt ignores exit codes", "card.go", "\t\tif found[s] != \"0\" {", "\t\tif _, ok := found[s]; !ok {"},
	{"author check unconfirmed ok", "card.go", "\t\t\t\tif ch.Kind == \"author\" && ch.Confirmed == \"\" {", "\t\t\t\tif false && ch.Kind == \"author\" {"},
	{"red none on behaviour card ok", "card.go", "\tif len(c.RedNone) > 0 && behaviour {", "\tif false {"},
	// freeze
	{"freeze change ignored", "check.go", "\t\t\tcase now.FrozenText() != frozen.Items[id].FrozenText():", "\t\t\tcase false && now != nil:"},
	{"freeze removal ignored", "check.go", "\t\t\tcase !ok:\n\t\t\t\tprobs = append(probs, fmt.Sprintf(\"%s: D%d was removed", "\t\t\tcase false && !ok:\n\t\t\t\tprobs = append(probs, fmt.Sprintf(\"%s: D%d was removed"},
	{"never-committed card ok", "check.go", "\t\t\tif status != \"\" && status != \"todo\" {", "\t\t\tif false {"},
	{"freeze follows renames", "git.go", "\tout, good := Git(root, \"log\", \"--diff-filter=A\", \"--format=%H\", \"--\", rel)", "\tout, good := Git(root, \"log\", \"--diff-filter=A\", \"--follow\", \"--format=%H\", \"--\", rel)"},
	{"legacy growth ignored", "check.go", "\t\t\tif len(grown) > 0 {", "\t\t\tif false {"},
	{"stale legacy entry ignored", "check.go", "\tif len(stale) > 0 && len(cards) == 0 {", "\tif false {"},
	{"legacy cards linted", "card.go", "\t\tif !legacy[filepath.Base(p)] {", "\t\tif true {"},
	// assert-guard
	{"assert drop ignored", "check.go", "\t\tif s.assertDel > s.assertAdd {", "\t\tif false {"},
	{"skip ignored", "check.go", "\t\tif s.skipAdd > 0 {", "\t\tif false {"},
	{"early exit ignored", "check.go", "\t\tif s.suspAdd > 0 {", "\t\tif false {"},
	{"deleted test ignored", "check.go", "\t\tif GlobMatch(f, t.Paths) && !allowed[f] {", "\t\tif false {"},
	{"header detection naive", "check.go", "\t\tif inHeader {", "\t\tif inHeader || strings.HasPrefix(line, \"--- \") || strings.HasPrefix(line, \"+++ \") {"},
	// gate
	{"partial gate recordable", "gate.go", "\tif len(o.only) > 0 && o.record != \"\" {", "\tif false {"},
	{"zero-test gate green", "gate.go", "\t\t\tcase s.Selected == 0:\n\t\t\t\tif code == 0 {\n\t\t\t\t\tcode = 1", "\t\t\tcase s.Selected == 0:\n\t\t\t\tif false {\n\t\t\t\t\tcode = 1"},
	// docs
	{"map size ignored", "docs.go", "n > d.AgentsMaxLines {", "n > d.AgentsMaxLines+1000 {"},
	{"broken link ignored", "docs.go", "\t\t\tcase path != \"\" && !exists(filepath.Join(dir, path)):", "\t\t\tcase false && !exists(filepath.Join(dir, path)):"},
	{"absolute link allowed", "docs.go", "\t\t\tcase strings.HasPrefix(path, \"/\"):", "\t\t\tcase false:"},
	{"inline code linked", "docs.go", "\t\tprose := inlineCode.ReplaceAllString(noFence, \"\")", "\t\tprose := noFence"},
	{"ignored paths flagged", "docs.go", "!mapPathExists(root, tok) && !gitIgnored(root, tok) {", "!mapPathExists(root, tok) {"},
	{"decisions unscoped", "docs.go", "\tif body, ok := sectionText(readFile(filepath.Join(root, d.Plan)), d.DecisionsHeading); ok {", "\tif body, ok := readFile(filepath.Join(root, d.Plan)), true; ok {"},
	{"progress order ignored", "docs.go", "\t\tif dates[i][1] > dates[i-1][1] {", "\t\tif false {"},
	{"registry symbol ignored", "docs.go", "\t\t\t} else if !symbolExists(root, files, tok) {", "\t\t\t} else if false {"},
	{"glossary ignored", "docs.go", "\t\t\t\tif regexp.MustCompile(`(^|[^\\w])` + regexp.QuoteMeta(term) + `([^\\w]|$)`).MatchString(line) {", "\t\t\t\tif false && regexp.MustCompile(term).MatchString(line) {"},
	// boundaries, config, versions, hooks
	{"boundary ignored", "check.go", "\t\t\t\t\tif re.MatchString(line) {\n\t\t\t\t\t\tprobs = append(probs, fmt.Sprintf(\"%s:%d: [%s]", "\t\t\t\t\tif false && re.MatchString(line) {\n\t\t\t\t\t\tprobs = append(probs, fmt.Sprintf(\"%s:%d: [%s]"},
	{"unknown keys accepted", "config.go", "\tif undecoded := md.Undecoded(); len(undecoded) > 0 {", "\tif undecoded := md.Undecoded(); false {\n\t\t_ = undecoded"},
	{"version mismatch accepted", "cli.go", "\tcase have != want:", "\tcase false:"},
	{"pseudo-version is a release", "cli.go", "\tif m := releaseRe.FindStringSubmatch(v); m != nil {\n\t\treturn m[1]\n\t}\n\treturn \"dev\"", "\treturn strings.TrimPrefix(v, \"v\")"},
	{"hook failures swallowed", "cli.go", "\t\t\tif len(failed) > 0 {\n\t\t\t\treturn failf(\"pre-commit:", "\t\t\tif false {\n\t\t\t\treturn failf(\"pre-commit:"},
	{"commit message unchecked", "scaffold.go", "\t\tif !conventional.MatchString(l) {", "\t\tif false {"},
	{"output race", "util.go", "\tw.mu.Lock()\n\tdefer w.mu.Unlock()\n", ""},
}

func main() {
	repo, _ := os.Getwd()
	if len(os.Args) > 1 {
		repo = os.Args[1]
	}
	var survived []string
	for _, m := range mutants {
		dir, err := os.MkdirTemp("", "keel-mut-")
		check(err)
		check(copyTree(repo, dir))
		path := filepath.Join(dir, "internal", "keel", m.file)
		src, err := os.ReadFile(path)
		check(err)
		if n := strings.Count(string(src), m.old); n != 1 {
			fmt.Fprintf(os.Stderr, "mutant %q: anchor found %d times in %s (must be exactly 1)\n", m.name, n, m.file)
			os.Exit(2)
		}
		check(os.WriteFile(path, []byte(strings.Replace(string(src), m.old, m.new, 1)), 0o644))
		args := []string{"test", "-count=1", "./internal/keel/"}
		if m.name == "output race" {
			args = []string{"test", "-race", "-count=1", "-run", "TestRunKeepsStdout", "./internal/keel/"}
		}
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		status := "killed"
		if err == nil {
			status = "SURVIVED"
			survived = append(survived, m.name)
		} else if strings.Contains(string(out), "[build failed]") || strings.Contains(string(out), "[setup failed]") {
			status = "INVALID"
			survived = append(survived, m.name+" (does not compile)")
			fmt.Fprintln(os.Stderr, string(out))
		}
		fmt.Printf("%-9s %s\n", status, m.name)
		os.RemoveAll(dir)
	}
	fmt.Printf("\n%d/%d killed; survived: %v\n", len(mutants)-len(survived), len(mutants), survived)
	if len(survived) > 0 {
		os.Exit(1)
	}
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		r, _ := filepath.Rel(src, p)
		if d.IsDir() && (r == ".git" || r == ".keel" || r == "dist") {
			return filepath.SkipDir
		}
		target := filepath.Join(dst, r)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, _ := d.Info()
		return os.WriteFile(target, b, info.Mode().Perm())
	})
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
