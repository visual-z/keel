package keel

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// End-to-end scenarios live in testdata/script/*.txtar: a real git
// repository per scenario, keel run through its real entry point, and the
// exit status and output it must produce. Each check is shown catching the
// violation it exists for, not only passing on clean input.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"keel": func() { os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr)) },
	})
}

// The fixture repository. tests/run.sh is a fake cargo: each `check NAME
// WANT` in tests/cases.sh is one test, passing when line 2 of calc.txt
// holds WANT (line 2, so a one-line fault is mid-file). Values: "broken" a
// compile error, "crash" a signal, "exit2" a failed test reported with the
// wrong exit code, "hang" a hang.
const fixtureRunner = `#!/bin/sh
filter="$1"; passed=0; failed=0
value="$(sed -n 2p calc.txt)"
[ -n "$FAULTY_CACHE" ] && value=0
case "$value" in
  broken) echo "error[E0425]: cannot find value"; exit 101 ;;
  crash) kill -9 $$ ;;
  hang) sleep 30; exit 0 ;;
esac
check() {
  case "$1" in *"$filter"*) ;; *) return ;; esac
  if [ "$value" = "$2" ]; then passed=$((passed+1)); echo "test $1 ... ok"
  else failed=$((failed+1)); echo "test $1 ... FAILED"; fi
}
. ./tests/cases.sh
echo "running $((passed+failed)) tests"
verdict=ok; [ $failed -gt 0 ] && verdict=FAILED
echo "test result: $verdict. $passed passed; $failed failed; 0 ignored; 0 measured; 0 filtered out"
[ "$value" = exit2 ] && exit 2
[ $failed -gt 0 ] && exit 101
exit 0
`

const fixtureCases = `check adds_two 4
check echoes_value "$value"
`

const fixtureConfig = `[keel]
version = "0.0.0-dev"

[[gate.step]]
name = "unit"
cmd = ["sh", "tests/run.sh", ""]
runner = "cargo"

[red]
runner = "cargo"
command = ["sh", "tests/run.sh", "{filter}"]
timeout_s = 60

[tests]
paths = ["tests/**"]
assert_patterns = ['\bcheck\b']
skip_patterns = ['\bskip\b']
suspicious_patterns = ['\bexit 0\b', '__eq__']

[docs]
agents_max_lines = 20
playbook_max_rules = 3
glossary_source_dirs = ["src"]

[[boundary.rule]]
name = "core is pure"
paths = ["src/core/**"]
forbid = ['^import (os|time)\b']
`

func TestScripts(t *testing.T) {
	gitEnv := []string{"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_CONFIG_NOSYSTEM=1"}
	goEnv := map[string]string{}
	for _, k := range []string{"GOCACHE", "GOPATH", "GOMODCACHE", "GOROOT"} {
		if out, err := exec.Command("go", "env", k).Output(); err == nil {
			goEnv[k] = strings.TrimSpace(string(out))
		}
	}
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		Setup: func(e *testscript.Env) error {
			home := filepath.Join(e.WorkDir, ".home")
			os.MkdirAll(home, 0o755)
			e.Setenv("HOME", home)
			for _, kv := range gitEnv {
				k, v, _ := strings.Cut(kv, "=")
				e.Setenv(k, v)
			}
			for k, v := range goEnv {
				e.Setenv(k, v)
			}
			e.Setenv("GOFLAGS", "-mod=mod")
			e.Setenv("GOTOOLCHAIN", "local")
			return nil
		},
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			// repo: a committed fixture repository; $BASE is its commit.
			"repo": func(ts *testscript.TestScript, neg bool, args []string) {
				for path, text := range map[string]string{"tests/run.sh": fixtureRunner, "tests/cases.sh": fixtureCases,
					"keel.toml": fixtureConfig, "calc.txt": "# calc\n4\n# end\n", ".gitignore": ".keel/\n.home/\n",
					"docs/tasks/README.md": "cards\n"} {
					full := ts.MkAbs(path)
					os.MkdirAll(filepath.Dir(full), 0o755)
					ts.Check(os.WriteFile(full, []byte(text), 0o644))
				}
				git(ts, "init", "-q", "-b", "main")
				commitAll(ts, "base")
				ts.Setenv("BASE", headSHA(ts))
			},
			// commit MSG [VAR]: commit everything; VAR receives the sha.
			"commit": func(ts *testscript.TestScript, neg bool, args []string) {
				commitAll(ts, args[0])
				if len(args) > 1 {
					ts.Setenv(args[1], headSHA(ts))
				}
			},
			// expand SRC DST: copy with $VAR substitution.
			"expand": func(ts *testscript.TestScript, neg bool, args []string) {
				text := os.Expand(ts.ReadFile(args[0]), ts.Getenv)
				full := ts.MkAbs(args[1])
				os.MkdirAll(filepath.Dir(full), 0o755)
				ts.Check(os.WriteFile(full, []byte(text), 0o644))
			},
			// setvalue TEXT: write calc.txt.
			"setvalue": func(ts *testscript.TestScript, neg bool, args []string) {
				ts.Check(os.WriteFile(ts.MkAbs("calc.txt"), []byte("# calc\n"+args[0]+"\n# end\n"), 0o644))
			},
			// gitsha VAR REV: VAR receives `git rev-parse REV`.
			"gitsha": func(ts *testscript.TestScript, neg bool, args []string) {
				out, err := gitOut(ts, "rev-parse", args[1])
				ts.Check(err)
				ts.Setenv(args[0], out)
			},
		},
	})
}

func git(ts *testscript.TestScript, args ...string) {
	if _, err := gitOut(ts, args...); err != nil {
		ts.Fatalf("git %v: %v", args, err)
	}
}

func gitOut(ts *testscript.TestScript, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = ts.Getenv("WORK")
	if d := ts.MkAbs("."); d != "" {
		cmd.Dir = d
	}
	var env []string
	for _, k := range []string{"HOME", "PATH", "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_CONFIG_NOSYSTEM"} {
		env = append(env, k+"="+ts.Getenv(k))
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", &os.PathError{Op: "git", Path: strings.Join(args, " "), Err: errString(strings.TrimSpace(string(out)))}
	}
	return strings.TrimSpace(string(out)), nil
}

type errString string

func (e errString) Error() string { return string(e) }

func commitAll(ts *testscript.TestScript, msg string) {
	git(ts, "add", "-A")
	// Scaffolding commits skip the repository's own hooks; hooks.txtar tests them.
	git(ts, "commit", "-q", "--no-verify", "--allow-empty", "-m", msg)
}

func headSHA(ts *testscript.TestScript) string {
	out, err := gitOut(ts, "rev-parse", "HEAD")
	ts.Check(err)
	return out
}
