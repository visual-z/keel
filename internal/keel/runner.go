package keel

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Summary is what a test run proved. Selected counts tests that actually
// executed (passed + failed); skipped ones proved nothing and are separate.
type Summary struct {
	Selected, Failed, Skipped int
	Invalid                   string // non-empty: the output cannot certify anything
	// FailedNames are the failed tests as the format names them, where it
	// names them (bun, cargo, go-json, junit); gate prints them and retries
	// them one by one.
	FailedNames []string
}

// Runners are output formats, not tools. Structured formats (go test -json,
// JUnit XML) are preferred; text parsers exist only for runners with no
// stable structured output (cargo test, bun test).
var Runners = []string{"cargo", "bun", "go-json", "junit", "custom"}

// redExit: the exit codes with which each format reports "tests failed".
// Anything else (usage errors, interrupts, crashes) is not a red.
var redExit = map[string][]int{
	"cargo":   {101},
	"bun":     {1},
	"go-json": {1},
	"junit":   {1},
}

func ints(re *regexp.Regexp, s string) []int {
	var out []int
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		n, _ := strconv.Atoi(m[1])
		out = append(out, n)
	}
	return out
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

var (
	cargoInvalid = regexp.MustCompile(`(?m)^error\[E\d+\]:|^error: could not compile|^error: linking with`)
	cargoResult  = regexp.MustCompile(`(?m)^test result: \w+\. (\d+) passed; (\d+) failed; (\d+) ignored`)
	cargoFailed  = regexp.MustCompile(`(?m)^test (\S+) \.\.\. FAILED\s*$`)
)

// ParseCargo counts from `test result:` lines, never `running N`: N includes
// #[ignore]d tests, and nested sub-executors print `running N` without a
// result. A failing test also prints `error: test failed, to rerun …`; only
// compiler and linker errors mean the code did not build.
func ParseCargo(out string) Summary {
	if cargoInvalid.MatchString(out) {
		return Summary{Invalid: "compile error (a compile error is not a behavioural red)"}
	}
	var s Summary
	for _, m := range cargoResult.FindAllStringSubmatch(out, -1) {
		p, _ := strconv.Atoi(m[1])
		f, _ := strconv.Atoi(m[2])
		i, _ := strconv.Atoi(m[3])
		s.Selected += p + f
		s.Failed += f
		s.Skipped += i
	}
	for _, m := range cargoFailed.FindAllStringSubmatch(out, -1) {
		s.FailedNames = append(s.FailedNames, m[1])
	}
	return s
}

var (
	bunPass    = regexp.MustCompile(`(?m)^\s*(\d+) pass\s*$`)
	bunFail    = regexp.MustCompile(`(?m)^\s*(\d+) fail\s*$`)
	bunSkip    = regexp.MustCompile(`(?m)^\s*(\d+) skip\s*$`)
	bunErrors  = regexp.MustCompile(`(?m)^\s*\d+ errors?\s*$`)
	bunRan     = regexp.MustCompile(`(?m)^Ran \d+ tests? across \d+ files?\.`)
	bunFailTag = regexp.MustCompile(`(?m)^\(fail\) `)
	bunFailed  = regexp.MustCompile(`(?m)^\(fail\) (.+?)(?: \[[\d.]+m?s\])?\s*$`)
)

// ParseBun requires exactly one normal summary. Loader errors, unhandled
// errors between tests and duplicated summaries cannot certify anything.
func ParseBun(out string) Summary {
	if bunErrors.MatchString(out) || strings.Contains(out, "Unhandled error between tests") {
		return Summary{Invalid: "load or unhandled errors"}
	}
	pass, fail := ints(bunPass, out), ints(bunFail, out)
	if len(pass) == 0 && len(fail) == 0 {
		return Summary{}
	}
	if len(pass) != 1 || len(fail) != 1 || !bunRan.MatchString(out) {
		return Summary{Invalid: "not exactly one normal bun test summary"}
	}
	s := Summary{Selected: pass[0] + fail[0], Failed: fail[0], Skipped: sum(ints(bunSkip, out))}
	if s.Failed > 0 && !bunFailTag.MatchString(out) {
		return Summary{Invalid: "a failure count with no failed test"}
	}
	for _, m := range bunFailed.FindAllStringSubmatch(out, -1) {
		s.FailedNames = append(s.FailedNames, m[1])
	}
	return s
}

type goEvent struct {
	Action  string
	Package string
	Test    string
	Output  string
}

var goBuildHeader = regexp.MustCompile(`^# \S+$`)

// ParseGoJSON reads `go test -json`. Build failures, and packages that fail
// outside any test (TestMain exits, init panics), cannot certify a test.
func ParseGoJSON(out string) Summary {
	var s Summary
	pkgFail := map[string]bool{}
	testFailIn := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "{") {
			if goBuildHeader.MatchString(line) {
				return Summary{Invalid: "build failed (a compile error is not a behavioural red)"}
			}
			continue
		}
		var e goEvent
		if json.Unmarshal([]byte(line), &e) != nil {
			continue
		}
		switch {
		case e.Action == "build-fail":
			return Summary{Invalid: "build failed (a compile error is not a behavioural red)"}
		case e.Action == "output" && (strings.Contains(e.Output, "[build failed]") || strings.Contains(e.Output, "[setup failed]")):
			return Summary{Invalid: "build failed (a compile error is not a behavioural red)"}
		case e.Test == "" && e.Action == "fail":
			pkgFail[e.Package] = true
		case e.Test != "" && e.Action == "pass":
			s.Selected++
		case e.Test != "" && e.Action == "fail":
			s.Selected++
			s.Failed++
			testFailIn[e.Package] = true
			s.FailedNames = append(s.FailedNames, e.Package+" "+e.Test)
		case e.Test != "" && e.Action == "skip":
			s.Skipped++
		}
	}
	for pkg := range pkgFail {
		if !testFailIn[pkg] {
			return Summary{Invalid: fmt.Sprintf("package %s failed outside any test", pkg)}
		}
	}
	return s
}

type junitCase struct {
	Name    string     `xml:"name,attr"`
	Class   string     `xml:"classname,attr"`
	Failure []struct{} `xml:"failure"`
	Error   []struct{} `xml:"error"`
	Skipped []struct{} `xml:"skipped"`
}

// ParseJUnit reads a JUnit XML report (pytest --junitxml, vitest/bun
// --reporter=junit, jest-junit, cargo-nextest). <error> means setup or
// collection broke, not that a test caught something, so it is invalid.
// The approach follows regression-witness: duplicates and empty reports
// are refused too.
func ParseJUnit(path string) Summary {
	f, err := os.Open(path)
	if err != nil {
		return Summary{Invalid: "no JUnit report was written (the command must write to {junit})"}
	}
	defer f.Close()
	dec := xml.NewDecoder(io.LimitReader(f, 256<<20))
	dec.Strict = true
	var s Summary
	seen := map[string]bool{}
	cases := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Summary{Invalid: "malformed JUnit report: " + err.Error()}
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "testsuite":
			for _, a := range se.Attr {
				if a.Name.Local == "errors" && a.Value != "" && a.Value != "0" {
					return Summary{Invalid: "the report records suite errors"}
				}
			}
		case "testcase":
			var c junitCase
			if err := dec.DecodeElement(&c, &se); err != nil {
				return Summary{Invalid: "malformed JUnit testcase: " + err.Error()}
			}
			cases++
			id := c.Class + "::" + c.Name
			if seen[id] {
				return Summary{Invalid: "duplicate testcase " + id}
			}
			seen[id] = true
			switch {
			case len(c.Error) > 0:
				return Summary{Invalid: "testcase " + id + " errored (setup or collection, not a behavioural red)"}
			case len(c.Skipped) > 0:
				s.Skipped++
			case len(c.Failure) > 0:
				s.Selected++
				s.Failed++
				s.FailedNames = append(s.FailedNames, c.Name)
			default:
				s.Selected++
			}
		}
	}
	if cases == 0 {
		return Summary{}
	}
	return s
}

func summarize(red RedConfig, runner, out, junitPath string) (Summary, error) {
	out = stripANSI(out)
	switch runner {
	case "cargo":
		return ParseCargo(out), nil
	case "bun":
		return ParseBun(out), nil
	case "go-json":
		return ParseGoJSON(out), nil
	case "junit":
		return ParseJUnit(junitPath), nil
	case "custom":
		if red.SelectedRe == "" || red.FailedRe == "" {
			return Summary{}, failf("runner `custom` needs red.selected_re and red.failed_re")
		}
		if red.InvalidRe != "" && regexp.MustCompile("(?m)"+red.InvalidRe).MatchString(out) {
			return Summary{Invalid: "matched invalid_re"}, nil
		}
		sel := sum(ints(regexp.MustCompile("(?m)"+red.SelectedRe), out))
		fail := sum(ints(regexp.MustCompile("(?m)"+red.FailedRe), out))
		return Summary{Selected: sel, Failed: fail}, nil
	}
	return Summary{}, failf("unknown runner `%s`; one of %s", runner, strings.Join(Runners, ", "))
}

// ---- commands

var existingRe = regexp.MustCompile(`^\{existing:(.+)\}$`)

// BuildCommand substitutes placeholders:
//
//	{filter}            the claim text, as one argument
//	{args}              the claim text split with shell rules (`-p crate name::`)
//	{junit}             path of the JUnit report this run must write
//	{existing:a,b}      the listed paths that exist in the run directory
func BuildCommand(template []string, filter, junit, cwd string) ([]string, error) {
	hasSel := false
	for _, a := range template {
		if strings.Contains(a, "{filter}") || a == "{args}" {
			hasSel = true
		}
	}
	if len(template) == 0 || !hasSel {
		return nil, failf("red.command must contain a `{filter}` or `{args}` argument")
	}
	var cmd []string
	for _, a := range template {
		switch {
		case a == "{args}":
			parts, err := ShellSplit(filter)
			if err != nil {
				return nil, failf("cannot split filter %q: %v", filter, err)
			}
			cmd = append(cmd, parts...)
		case existingRe.MatchString(a):
			for _, p := range strings.Split(existingRe.FindStringSubmatch(a)[1], ",") {
				if p = strings.TrimSpace(p); p != "" && exists(filepath.Join(cwd, p)) {
					cmd = append(cmd, p)
				}
			}
		default:
			cmd = append(cmd, strings.NewReplacer("{filter}", filter, "{junit}", junit).Replace(a))
		}
	}
	return cmd, nil
}

// ShellSplit splits like POSIX sh word splitting with quotes and backslashes.
func ShellSplit(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord, quote := false, rune(0)
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc, inWord = false, true
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			if r == '"' {
				quote = 0
			} else if r == '\\' {
				esc = true
			} else {
				cur.WriteRune(r)
			}
		case r == '\\':
			esc, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || esc {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

// TestEnv: `isolated` runs happen in throwaway worktrees and may use red.env
// (a private build cache). The working tree never does: a cache shared with
// a fault-injected checkout hands it the faulty build, because build tools
// compare mtimes and a fresh checkout's sources look older than the cache.
func TestEnv(root string, red RedConfig, isolated bool, junit string) []string {
	env := os.Environ()
	// Coloured summaries break count parsers and turn a real red into "matched 0".
	env = append(env, "NO_COLOR=1", "FORCE_COLOR=0", "CARGO_TERM_COLOR=never")
	if os.Getenv("CI") == "" {
		env = append(env, "CI=1")
	}
	if isolated {
		cache := filepath.Join(root, ".keel", "cache")
		_ = os.MkdirAll(cache, 0o755)
		for k, v := range red.Env {
			env = append(env, k+"="+strings.NewReplacer("{cache}", cache, "{junit}", junit).Replace(v))
		}
	}
	return env
}

type RunOutcome struct {
	Res Result
	Sum Summary
}

// RunFilter runs one selection of tests in tree (the root or a worktree).
func RunFilter(root string, red RedConfig, tree, filter string) (RunOutcome, error) {
	cwd := filepath.Join(tree, red.Cwd)
	junitDir, err := os.MkdirTemp("", "keel-junit-")
	if err != nil {
		return RunOutcome{}, err
	}
	defer os.RemoveAll(junitDir)
	junit := filepath.Join(junitDir, "report.xml")
	cmd, err := BuildCommand(red.Command, filter, junit, cwd)
	if err != nil {
		return RunOutcome{}, err
	}
	isolated := filepath.Clean(tree) != filepath.Clean(root)
	res, err := Run(cwd, TestEnv(root, red, isolated, junit), time.Duration(red.TimeoutS)*time.Second, "", cmd[0], cmd[1:]...)
	if err != nil {
		return RunOutcome{}, failf("cannot run `%s` in %s: %v", cmd[0], cwd, err)
	}
	s, err := summarize(red, red.Runner, res.Out, junit)
	return RunOutcome{res, s}, err
}

func tail(out string, n int) string {
	ls := lines(strings.TrimRight(stripANSI(out), "\n"))
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	for i := range ls {
		ls[i] = "      " + ls[i]
	}
	return strings.Join(ls, "\n")
}

func redExitOK(red RedConfig, code int) bool {
	allowed := red.RedExit
	if len(allowed) == 0 {
		allowed = redExit[red.Runner]
	}
	if len(allowed) == 0 {
		return code > 0
	}
	for _, c := range allowed {
		if c == code {
			return true
		}
	}
	return false
}

// RequireRed: the run must be a normal test run in which a selected test
// failed, and the exit code must say so too.
func RequireRed(red RedConfig, o RunOutcome, where string) error {
	r, s := o.Res, o.Sum
	switch {
	case r.TimedOut:
		return failf("%s: the test run timed out; a timeout is not a red", where)
	case r.Signaled:
		return failf("%s: the runner was killed by a signal; a crash is not a red", where)
	case s.Invalid != "":
		return failf("%s: %s\n%s", where, s.Invalid, tail(r.Out, 12))
	case s.Selected == 0:
		return failf("%s: the filter selected 0 tests; a missing or skipped test is not red evidence\n%s", where, tail(r.Out, 12))
	case r.Code == 0:
		return failf("%s: the test PASSED; it does not detect what the card claims", where)
	case s.Failed == 0:
		return failf("%s: non-zero exit (%d) without a failed test; not a behavioural red\n%s", where, r.Code, tail(r.Out, 12))
	case !redExitOK(red, r.Code):
		return failf("%s: exit %d is not how `%s` reports failed tests; the run was abnormal\n%s", where, r.Code, red.Runner, tail(r.Out, 12))
	}
	return nil
}

func RequireGreen(o RunOutcome, where string) error {
	r, s := o.Res, o.Sum
	if r.Code != 0 || r.TimedOut || r.Signaled || s.Invalid != "" || s.Failed > 0 {
		inv := ""
		if s.Invalid != "" {
			inv = " (" + s.Invalid + ")"
		}
		return failf("%s: expected green after the fix, got exit %d%s\n%s", where, r.Code, inv, tail(r.Out, 12))
	}
	if s.Selected == 0 {
		return failf("%s: the filter selected 0 tests at the fixed tree", where)
	}
	return nil
}
