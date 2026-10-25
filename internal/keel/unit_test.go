package keel

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestCargoCountsOnlyTestsThatRanNeverIgnoredOrResultlessSubExecutors(t *testing.T) {
	out := "running 3 tests\ntest a ... ok\ntest b ... ignored\ntest c ... ok\n" +
		"test result: ok. 2 passed; 0 failed; 1 ignored; 0 measured; 0 filtered out\n" +
		"running 4 tests\n" + // a nested sub-executor: running N, then no result line
		"running 1 test\ntest d ... FAILED\n" +
		"test result: FAILED. 0 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out\n"
	if s := ParseCargo(out); counts(s) != (tally{Selected: 3, Failed: 1, Skipped: 1}) {
		t.Fatalf("got %+v", s)
	}
}

func TestARealCargoTestFailureIsRedNotACompileError(t *testing.T) {
	out := "running 1 test\ntest damage::tests::coalesces ... FAILED\n\nfailures:\n    damage::tests::coalesces\n\n" +
		"test result: FAILED. 0 passed; 1 failed; 0 ignored; 0 measured; 226 filtered out; finished in 0.00s\n\n" +
		"error: test failed, to rerun pass `-p core --lib`\nerror: 1 target failed:\n    `-p core --lib`\n"
	if s := ParseCargo(out); counts(s) != (tally{Selected: 1, Failed: 1}) {
		t.Fatalf("got %+v", s)
	}
	for _, bad := range []string{"error[E0425]: cannot find value", "error: could not compile `core` (lib test) due to 1 previous error", "error: linking with `cc` failed"} {
		if ParseCargo(bad).Invalid == "" {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestACargoFilterThatOnlyHitsIgnoredTestsSelectsNothing(t *testing.T) {
	s := ParseCargo("running 1 test\ntest a ... ignored\ntest result: ok. 0 passed; 0 failed; 1 ignored; 0 measured; 9 filtered out\n")
	if s.Selected != 0 || s.Skipped != 1 {
		t.Fatalf("got %+v", s)
	}
}

// tally is what a summary counted, comparable with ==.
type tally struct {
	Selected, Failed, Skipped int
	Invalid                   string
}

func counts(s Summary) tally { return tally{s.Selected, s.Failed, s.Skipped, s.Invalid} }

func TestEachFormatNamesItsFailedTests(t *testing.T) {
	cargo := "running 2 tests\ntest a::ok ... ok\ntest a::bad ... FAILED\ntest result: FAILED. 1 passed; 1 failed; 0 ignored; 0 measured; 0 filtered out\n"
	bun := "(fail) suite > a timing test [5002.08ms]\n(fail) plain\n 1 pass\n 2 fail\nRan 3 tests across 1 file. [9ms]\n"
	goj := `{"Action":"fail","Package":"p","Test":"TestC/sub"}` + "\n" + `{"Action":"fail","Package":"p","Test":"TestC"}`
	junit := `<testsuite><testcase classname="c" name="x"><failure/></testcase></testsuite>`
	for name, c := range map[string]struct {
		got  []string
		want []string
	}{
		"cargo": {ParseCargo(cargo).FailedNames, []string{"a::bad"}},
		"bun":   {ParseBun(bun).FailedNames, []string{"suite > a timing test", "plain"}},
		"go":    {ParseGoJSON(goj).FailedNames, []string{"p TestC/sub", "p TestC"}},
		"junit": {ParseJUnit(writeTemp(t, junit)).FailedNames, []string{"x"}},
	} {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s: got %q, want %q", name, c.got, c.want)
		}
	}
}

const bunRed = "(fail) fixture proof\n 0 pass\n 1 fail\nRan 1 test across 1 file. [1ms]\n"

// Ported from xeno's red-evidence tests: a normal failed test, and nothing
// that merely looks like one.
func TestBunSummariesRequireANormalFailedTestAndRejectMixedLoaderOrDuplicateSummaries(t *testing.T) {
	if s := ParseBun(bunRed); counts(s) != (tally{Selected: 1, Failed: 1}) {
		t.Fatalf("got %+v", s)
	}
	for _, bad := range []string{"1 error\n" + bunRed, "Unhandled error between tests\n" + bunRed, bunRed + bunRed,
		strings.Replace(bunRed, "(fail)", "(pass)", 1), strings.Replace(bunRed, "Ran 1 test across 1 file.", "", 1)} {
		if ParseBun(bad).Invalid == "" {
			t.Errorf("must be invalid:\n%s", bad)
		}
	}
	if s := ParseBun(strings.Replace(bunRed, "1 fail", "0 fail", 1)); s.Failed != 0 {
		t.Errorf("zero fails: %+v", s)
	}
	if s := ParseBun(stripANSI("\x1b[31m" + bunRed + "\x1b[0m")); s.Invalid != "" || s.Failed != 1 {
		t.Errorf("ANSI must be stripped: %+v", s)
	}
}

func TestGoJSONCountsVerdictsNotRunsAndRejectsBuildAndPackageFailures(t *testing.T) {
	ev := func(action, test string) string {
		return `{"Action":"` + action + `","Package":"p","Test":"` + test + `"}`
	}
	out := strings.Join([]string{ev("run", "TestA"), ev("pass", "TestA"), ev("run", "TestB"), ev("skip", "TestB"),
		ev("run", "TestC"), ev("fail", "TestC/sub"), ev("fail", "TestC"), `{"Action":"fail","Package":"p"}`}, "\n")
	if s := ParseGoJSON(out); counts(s) != (tally{Selected: 3, Failed: 2, Skipped: 1}) {
		t.Fatalf("got %+v", s)
	}
	for _, bad := range []string{`{"Action":"build-fail","Package":"p"}`, "# p\n./x.go:3:1: syntax error",
		`{"Action":"output","Package":"p","Output":"FAIL\tp [build failed]\n"}`,
		ev("pass", "TestA") + "\n" + `{"Action":"fail","Package":"p"}`} {
		if ParseGoJSON(bad).Invalid == "" {
			t.Errorf("must be invalid: %s", bad)
		}
	}
}

func writeTemp(t *testing.T, text string) string {
	p := filepath.Join(t.TempDir(), "r.xml")
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestJUnitTreatsErrorsDuplicatesAndMissingReportsAsInvalid(t *testing.T) {
	ok := `<testsuites><testsuite name="s" errors="0"><testcase classname="c" name="a"/>` +
		`<testcase classname="c" name="b"><failure message="x"/></testcase>` +
		`<testcase classname="c" name="z"><skipped/></testcase></testsuite></testsuites>`
	if s := ParseJUnit(writeTemp(t, ok)); counts(s) != (tally{Selected: 2, Failed: 1, Skipped: 1}) {
		t.Fatalf("got %+v", s)
	}
	for name, bad := range map[string]string{
		"error":     `<testsuite><testcase classname="c" name="a"><error/></testcase></testsuite>`,
		"duplicate": `<testsuite><testcase classname="c" name="a"/><testcase classname="c" name="a"/></testsuite>`,
		"suite err": `<testsuite errors="1"><testcase classname="c" name="a"/></testsuite>`,
		"malformed": `<testsuite><testcase`,
	} {
		if ParseJUnit(writeTemp(t, bad)).Invalid == "" {
			t.Errorf("%s must be invalid", name)
		}
	}
	if ParseJUnit(filepath.Join(t.TempDir(), "missing.xml")).Invalid == "" {
		t.Error("a missing report must be invalid")
	}
	if s := ParseJUnit(writeTemp(t, `<testsuites/>`)); s.Selected != 0 || s.Invalid != "" {
		t.Errorf("an empty report selects nothing: %+v", s)
	}
}

// Ported from xeno: fixed seed, exact filters, and no mutable revisions.
func TestFixedSeedRedDeclarationsKeepExactFiltersAndRejectMutableOrOptionShapedRevisions(t *testing.T) {
	r := rand.New(rand.NewSource(1212))
	for n := 0; n < 250; n++ {
		b := make([]byte, 40)
		for i := range b {
			b[i] = "0123456789abcdef"[r.Intn(16)]
		}
		rev, filter := string(b), "fixture ("+strings.Repeat("x", n%7)+") / exact text"
		c := ParseCard("description\nRED: "+rev+" "+filter+"\n", "card.md")
		if len(c.Errors) != 0 || !reflect.DeepEqual(c.Reds, []RedClaim{{Revision: rev, Filter: filter, Line: 2}}) {
			t.Fatalf("got %+v %v", c.Reds, c.Errors)
		}
		for _, bad := range []string{"HEAD", "--all", "a;touch-file", "<commit>", rev[:6]} {
			if c := ParseCard("RED: "+bad+" "+filter, "card.md"); len(c.Errors) != 1 || len(c.Reds) != 0 {
				t.Fatalf("%q must be invalid: %+v", bad, c)
			}
		}
		if c := ParseCard("RED: "+rev+" ", "card.md"); len(c.Errors) != 1 {
			t.Fatalf("an empty filter must be invalid")
		}
	}
}

func TestClaimsInsideCodeFencesAndCommentsAreNotClaimsAndSuffixesAreStripped(t *testing.T) {
	c := ParseCard("```\nRED: abcdef1 not-a-claim\n```\n<!--\nRED: <commit> <filter>\n-->\nRED: abcdef1 real one  # D1, D2\nRED(web): abcdef2 web one\n", "x.md")
	want := []RedClaim{{Revision: "abcdef1", Filter: "real one", Line: 7}, {Revision: "abcdef2", Filter: "web one", Profile: "web", Line: 8}}
	if len(c.Errors) != 0 || !reflect.DeepEqual(c.Reds, want) {
		t.Fatalf("got %+v %v", c.Reds, c.Errors)
	}
}

func TestReceiptsMustShowEveryStepExitingZero(t *testing.T) {
	steps := []string{"web-paint", "build", "test"}
	cases := map[string]bool{
		"web-paint=0 build=0 test=0[812p/0f/2skip]": true,
		"build=0 test=0":                      false,
		"web-paint=0 build=0 test=1":          false,
		"web-paint=0 build=0 test=0[0 tests]": true,
	}
	for r, want := range cases {
		if ReceiptCovers(" "+r, ReceiptRule{Steps: steps}) != want {
			t.Errorf("%q: want %v", r, want)
		}
	}
}

func TestOnlyAnExternalStepMayShowAFailureItsAnchorShares(t *testing.T) {
	rule := ReceiptRule{Steps: []string{"test", "deny"}, External: map[string]bool{"deny": true}}
	if !ReceiptCovers(" test=0[3p/0f] deny=1(base)", rule) {
		t.Error("an external step failing at its anchor too must not block")
	}
	if ReceiptCovers(" test=1(base) deny=0", rule) {
		t.Error("a non-external step must exit 0")
	}
	if ReceiptCovers(" test=0 deny=1", rule) {
		t.Error("an external failure the anchor does not share blocks")
	}
}

func TestArgsSplitWithShellRulesAndExistingKeepsOnlyPresentPaths(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "test"), 0o755)
	got, err := BuildCommand([]string{"cargo", "test", "{args}", "{existing:./test,./eval/test}", "--out={junit}"},
		`-p core --test 'a b' name::`, "/r.xml", dir)
	want := []string{"cargo", "test", "-p", "core", "--test", "a b", "name::", "./test", "--out=/r.xml"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q %v", got, err)
	}
	if got, _ := BuildCommand([]string{"x", "-k", "{filter}"}, "a b", "", dir); !reflect.DeepEqual(got, []string{"x", "-k", "a b"}) {
		t.Errorf("filter is one argument: %q", got)
	}
	if _, err := BuildCommand([]string{"x"}, "a", "", dir); err == nil {
		t.Error("a command with no selection must be refused")
	}
	if _, err := ShellSplit(`a 'b`); err == nil {
		t.Error("an unterminated quote must be refused")
	}
}

func TestGlobsCrossDirectoriesAndDoubleStarMatchesZeroDirectories(t *testing.T) {
	for _, c := range []struct {
		path, pat string
		want      bool
	}{{"a/b/c_test.go", "**/*_test.go", true}, {"c_test.go", "**/*_test.go", true}, {"tests/x/y.rs", "tests/**", true},
		{"src/core/a.ts", "src/core/**", true}, {"src/other/a.ts", "src/core/**", false}, {"web/src/a.test.tsx", "**/*.test.*", true}} {
		if GlobMatch(c.path, []string{c.pat}) != c.want {
			t.Errorf("%s ~ %s: want %v", c.path, c.pat, c.want)
		}
	}
}

func TestAVersionMismatchRefusesToRunAndAMissingPinIsAnError(t *testing.T) {
	dir := t.TempDir()
	old := Version
	t.Cleanup(func() { Version = old })
	write := func(s string) { os.WriteFile(filepath.Join(dir, configFile), []byte(s), 0o644) }
	Version = "1.2.0"
	t.Setenv("KEEL_TOOLCHAIN", "local")
	t.Setenv("KEEL_HOME", t.TempDir())
	write("[keel]\nversion = \"1.1.0\"\n")
	if _, err := loadPinned(dir, os.Stderr); err == nil || !strings.Contains(err.Error(), "pins 1.1.0") {
		t.Fatalf("mismatch must fail: %v", err)
	}
	write("[keel]\nversion = \"1.2.0\"\n")
	if _, err := loadPinned(dir, os.Stderr); err != nil {
		t.Fatalf("matching pin must pass: %v", err)
	}
	write("[project]\nname = \"x\"\n")
	if _, err := loadPinned(dir, os.Stderr); err == nil || !strings.Contains(err.Error(), "must pin") {
		t.Fatalf("missing pin must fail: %v", err)
	}
	write("[keel]\nversion = \"1.2.0\"\n[gaet]\nx = 1\n")
	if _, err := loadPinned(dir, os.Stderr); err == nil || !strings.Contains(err.Error(), "unknown key") {
		t.Fatalf("a misspelt section must fail: %v", err)
	}
}

func TestAProfileInheritsUnsetFieldsAndCanClearAnInheritedList(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, configFile), []byte(`[keel]
version = "1.0.0"
[red]
runner = "cargo"
command = ["cargo", "test", "{args}"]
link = ["node_modules"]
timeout_s = 99
[red.profile.web]
runner = "junit"
cwd = "web"
link = []
`), 0o644)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	web, _ := cfg.RedFor("web")
	if web.Runner != "junit" || web.Cwd != "web" || web.TimeoutS != 99 || len(web.Link) != 0 || web.Command[0] != "cargo" {
		t.Fatalf("got %+v", web)
	}
	if _, err := cfg.RedFor("ios"); err == nil || !strings.Contains(err.Error(), "declared: web") {
		t.Fatalf("unknown profile: %v", err)
	}
}

func TestMapPathsMayNameSymbolsAndGoPatterns(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg/release"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg/mod.rs"), nil, 0o644)
	for tok, want := range map[string]bool{"pkg/release.Parse": true, "pkg/...": true, "pkg/mod.rs::item": true, "pkg/gone": false} {
		if mapPathExists(dir, tok) != want {
			t.Errorf("%s: want %v", tok, want)
		}
	}
}

func TestCommitMessagesFollowConventionalCommits(t *testing.T) {
	dir := t.TempDir()
	for msg, ok := range map[string]bool{"feat(api): accept tokens\n": true, "# comment\nfix: x\n": true,
		"Merge branch 'a'\n": true, "updated stuff\n": false, "\n": false, "feat:missing space\n": false} {
		p := filepath.Join(dir, "m")
		os.WriteFile(p, []byte(msg), 0o644)
		if (CommitMsg(p) == nil) != ok {
			t.Errorf("%q: want ok=%v", msg, ok)
		}
	}
}

func TestOnlyACleanReleaseVersionCountsAsARelease(t *testing.T) {
	for v, want := range map[string]string{"v1.2.3": "1.2.3", "1.2.3": "1.2.3", "(devel)": "dev", "": "dev",
		"v0.0.0-20261023111357-0243325c703a+dirty": "dev", "v1.2.4-0.20261023111357-0243325c703a": "dev", "v1.2.3-rc.1": "dev"} {
		if got := releaseOrDev(v); got != want {
			t.Errorf("%q: got %q want %q", v, got, want)
		}
	}
}

func TestRunKeepsStdoutAndStderrInTheCombinedOutput(t *testing.T) {
	for i := 0; i < 20; i++ {
		r, err := Run(t.TempDir(), nil, 0, "", "sh", "-c", "for n in 1 2 3 4 5; do echo out$n; echo err$n >&2; done")
		if err != nil || r.Code != 0 {
			t.Fatal(err, r.Code)
		}
		for n := 1; n <= 5; n++ {
			for _, s := range []string{"out", "err"} {
				if !strings.Contains(r.Out, s+string(rune('0'+n))) {
					t.Fatalf("combined output lost %s%d: %q", s, n, r.Out)
				}
			}
		}
		if strings.Contains(r.Stdout, "err") {
			t.Fatalf("stdout must not contain stderr: %q", r.Stdout)
		}
	}
}

func TestRetainRefusesToMoveAnExistingRefOrASymbolicOne(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=f@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=f@example.invalid")
	run := func(args ...string) string {
		out, msg, ok := GitIn(dir, "", env, args...)
		if !ok {
			t.Fatalf("git %v: %s", args, msg)
		}
		return out
	}
	run("init", "-q")
	run("commit", "-q", "--allow-empty", "-m", "a")
	a := run("rev-parse", "HEAD")
	run("commit", "-q", "--allow-empty", "-m", "b")
	b := run("rev-parse", "HEAD")
	const prefix = "refs/keel/red/"
	run("update-ref", prefix+b, a)
	if err := Retain(dir, prefix, b); err == nil || !strings.Contains(err.Error(), "refusing to move it") {
		t.Fatalf("a conflicting ref must be refused: %v", err)
	}
	if got := run("rev-parse", prefix+b); got != a {
		t.Fatalf("the ref moved to %s", got)
	}
	run("update-ref", "-d", prefix+b)
	run("symbolic-ref", prefix+b, "refs/heads/missing")
	if err := Retain(dir, prefix, b); err == nil {
		t.Fatal("a symbolic ref must be refused")
	}
	run("symbolic-ref", "--delete", prefix+b)
	if err := Retain(dir, prefix, b); err != nil {
		t.Fatalf("a free ref must be created: %v", err)
	}
	if RefStateOf(dir, prefix, b) != RefRetained {
		t.Fatal("not retained")
	}
}

// fakeRelease serves a release the way GitHub does: an archive per platform
// and a checksums.txt. corrupt serves an archive that does not match.
func fakeRelease(t *testing.T, version string, corrupt bool) string {
	name := fmt.Sprintf("keel_%s_%s_%s", version, runtime.GOOS, runtime.GOARCH)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("#!/bin/sh\necho pinned\n")
	tw.WriteHeader(&tar.Header{Name: name + "/keel", Mode: 0o755, Size: int64(len(body))})
	tw.Write(body)
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	if corrupt {
		archive = append([]byte{}, archive...)
		archive[len(archive)-1] ^= 0xff
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v" + version + "/checksums.txt":
			fmt.Fprintf(w, "%s  %s.tar.gz\n", hex.EncodeToString(sum[:]), name)
		case "/v" + version + "/" + name + ".tar.gz":
			w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestAMismatchRunsThePinnedReleaseInstallingItFromAVerifiedDownload(t *testing.T) {
	dir, home := t.TempDir(), t.TempDir()
	old, oldExec, oldInv := Version, execFn, invocation
	t.Cleanup(func() { Version, execFn, invocation = old, oldExec, oldInv })
	Version = "1.2.0"
	invocation = []string{"gate", "--record", "x.md"}
	var ran []string
	var ranEnv []string
	execFn = func(bin string, argv, env []string) error { ran, ranEnv = argv, env; return nil }
	t.Setenv("KEEL_HOME", home)
	t.Setenv("KEEL_TOOLCHAIN", "")
	t.Setenv(switchedEnv, "")
	os.WriteFile(filepath.Join(dir, configFile), []byte("[keel]\nversion = \"1.1.0\"\n"), 0o644)

	t.Setenv("KEEL_DOWNLOAD_BASE", fakeRelease(t, "1.1.0", true))
	if _, err := loadPinned(dir, io.Discard); err == nil || !strings.Contains(err.Error(), "does not match checksums.txt") {
		t.Fatalf("a corrupt download must be refused: %v", err)
	}
	if exists(toolchainPath("1.1.0")) || ran != nil {
		t.Fatal("a refused download must install and run nothing")
	}

	t.Setenv("KEEL_DOWNLOAD_BASE", fakeRelease(t, "1.1.0", false))
	if _, err := loadPinned(dir, io.Discard); err != nil {
		t.Fatalf("switch: %v", err)
	}
	if want := []string{toolchainPath("1.1.0"), "gate", "--record", "x.md"}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("ran %q, want %q", ran, want)
	}
	if !contains(ranEnv, switchedEnv+"=1.1.0") {
		t.Fatal("the switched process must know it was switched to")
	}
	if b, _ := os.ReadFile(toolchainPath("1.1.0")); !strings.Contains(string(b), "pinned") {
		t.Fatal("the release binary was not installed")
	}

	// Installed now: no download needed (the server is gone).
	ran = nil
	t.Setenv("KEEL_DOWNLOAD_BASE", "http://127.0.0.1:1")
	if _, err := loadPinned(dir, io.Discard); err != nil || ran == nil {
		t.Fatalf("an installed release must run without downloading: %v", err)
	}

	// A switched binary that still disagrees refuses instead of looping.
	ran = nil
	t.Setenv(switchedEnv, "1.1.0")
	if _, err := loadPinned(dir, io.Discard); err == nil || !strings.Contains(err.Error(), "already switched") || ran != nil {
		t.Fatalf("a second switch must refuse: %v", err)
	}
}
