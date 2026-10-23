// Package keel is the workspace's trusted kernel: it re-derives every claim a
// task card makes from git and from real exit codes instead of believing the
// narrative, the way a proof checker re-checks a proof.
package keel

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
)

// Version is set at release time with -ldflags "-X …/internal/keel.Version=1.2.3".
var Version = "dev"

// BinaryVersion is this binary's release version, or "dev" for a source build.
func BinaryVersion() string {
	if Version != "dev" {
		return releaseOrDev(Version)
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		return releaseOrDev(bi.Main.Version)
	}
	return "dev"
}

var releaseRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+)$`)

// releaseOrDev: only a clean X.Y.Z is a release. `go build` in a checkout
// stamps pseudo-versions like 0.0.0-20261023111357-0243325c703a+dirty; those
// are source builds and must not be mistaken for a release.
func releaseOrDev(v string) string {
	if m := releaseRe.FindStringSubmatch(v); m != nil {
		return m[1]
	}
	return "dev"
}

// PinVersion is what `init` writes into keel.toml.
func PinVersion() string {
	if v := BinaryVersion(); v != "dev" {
		return v
	}
	return "0.0.0-dev"
}

const usage = `keel — re-derive task-card claims from git and real exit codes

usage: keel [--root DIR] <command> [args]

  init --name N --stack S      turn an empty repository into a keel project
  adopt [--stack S]            bring keel into an existing project (legacy list)
  new <type> <slug> [--light]  open a card from the template
  status [--all]               cards by status
  lint [card…]                 card structure, bound checks, finished-card evidence
  freeze-check [card…]         done-definitions unchanged since the freeze point
  verify-red <card> [--at REV] [--repeat N] [--no-green]
                               re-run RED claims and injected faults: red, then green
  audit-red [--retain] [--json]
                               every claim names a retained commit (no test re-run)
  snapshot <path…>             commit HEAD + paths as a retained RED snapshot
  assert-guard [--base B]      the diff weakens no test
  gate [--record CARD] [--keep-going] [--only a,b]
                               run every gate step, print a receipt
  check-docs                   map size, links, registries, decisions, glossary
  check-boundaries             regex boundary rules
  mutate [--base B]            the configured diff-scoped mutation tool
  hook pre-commit|commit-msg F the git hooks
  version
`

// Main runs keel and returns the process exit code: 0 ok, 1 a check failed,
// 2 usage error.
func Main(args []string, stdout, stderr io.Writer) int {
	root := ""
	for len(args) > 0 && strings.HasPrefix(args[0], "--root") {
		if args[0] == "--root" && len(args) > 1 {
			root, args = args[1], args[2:]
		} else if v, ok := strings.CutPrefix(args[0], "--root="); ok {
			root, args = v, args[1:]
		} else {
			break
		}
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" || cmd == "--version" {
		fmt.Fprintln(stdout, "keel", BinaryVersion())
		return 0
	}
	root = findRoot(root)
	err := dispatch(root, cmd, rest, stdout, stderr)
	if err == nil {
		return 0
	}
	var u *usageErr
	if errors.As(err, &u) {
		fmt.Fprintf(stderr, "keel %s: %s\n", cmd, u.msg)
		return 2
	}
	fmt.Fprintf(stderr, "FAIL  %s\n", err.Error())
	return 1
}

type usageErr struct{ msg string }

func (u *usageErr) Error() string { return u.msg }

func usagef(f string, a ...any) error { return &usageErr{fmt.Sprintf(f, a...)} }

func findRoot(explicit string) string {
	if explicit != "" {
		abs, _ := filepath.Abs(explicit)
		return abs
	}
	if v := os.Getenv("KEEL_ROOT"); v != "" {
		abs, _ := filepath.Abs(v)
		return abs
	}
	wd, _ := os.Getwd()
	if out, ok := Git(wd, "rev-parse", "--show-toplevel"); ok {
		return out
	}
	return wd
}

// parse lets flags and positionals interleave (`verify-red card.md --no-green`).
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usagef("%v", err)
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// loadPinned loads keel.toml and refuses to run with a different release
// than the one the repository pins: results must not depend on which keel
// happens to be on PATH.
func loadPinned(root string, stderr io.Writer) (*Config, error) {
	cfg, err := LoadConfig(root)
	if err != nil {
		return nil, err
	}
	want, have := cfg.Keel.Version, BinaryVersion()
	switch {
	case want == "":
		return nil, failf("keel.toml must pin a release: add\n\n  [keel]\n  version = \"%s\"\n", PinVersion())
	case have == "dev":
		fmt.Fprintf(stderr, "keel: source build; keel.toml pins %s\n", want)
	case have != want:
		return nil, failf("this is keel %s but keel.toml pins %s; install it with\n  mise use github:visual-z/keel@%s\n  or: go install github.com/visual-z/keel/cmd/keel@v%s", have, want, want, want)
	}
	return cfg, nil
}

func dispatch(root, cmd string, args []string, stdout, stderr io.Writer) error {
	fs := newFlags(cmd, stderr)
	switch cmd {
	case "init":
		name := fs.String("name", "", "project name")
		stack := fs.String("stack", "", "preset: "+strings.Join(presetNames(), ", "))
		if _, err := parse(fs, args); err != nil {
			return err
		}
		if *name == "" || *stack == "" {
			return usagef("--name and --stack are required")
		}
		return Init(root, *name, *stack, stdout)
	case "adopt":
		stack := fs.String("stack", "", "preset used when keel.toml is missing")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		return Adopt(root, *stack, stdout)
	}
	cfg, err := loadPinned(root, stderr)
	if err != nil {
		return err
	}
	switch cmd {
	case "new":
		light := fs.Bool("light", false, "use the light template")
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		if len(pos) != 2 {
			return usagef("usage: keel new <type> <slug> [--light]")
		}
		return NewCard(root, cfg, pos[0], pos[1], *light, stdout)
	case "status":
		all := fs.Bool("all", false, "include done cards")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		return Status(root, cfg, *all, stdout)
	case "lint":
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		return Lint(root, cfg, pos, stdout)
	case "freeze-check":
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		return FreezeCheck(root, cfg, pos, stdout)
	case "verify-red":
		at := fs.String("at", "", "revision injected faults apply to (default HEAD)")
		repeat := fs.Int("repeat", 0, "runs per claim; all must agree (default red.repeat)")
		noGreen := fs.Bool("no-green", false, "skip the post-fix green run")
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		if len(pos) != 1 {
			return usagef("usage: keel verify-red <card>")
		}
		return VerifyRed(root, cfg, pos[0], verifyOpts{at: *at, noGreen: *noGreen, repeat: *repeat}, stdout)
	case "audit-red":
		retain := fs.Bool("retain", false, "retain every claimed commit (all or nothing)")
		asJSON := fs.Bool("json", false, "machine-readable report")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		return AuditRed(root, cfg, *retain, *asJSON, stdout)
	case "snapshot":
		pos, err := parse(fs, args)
		if err != nil {
			return err
		}
		if len(pos) == 0 {
			return usagef("usage: keel snapshot <path…>")
		}
		return Snapshot(root, cfg, pos, stdout)
	case "assert-guard":
		base := fs.String("base", "", "branch to compare with (default guard.base)")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		return AssertGuard(root, cfg, *base, stdout)
	case "gate":
		record := fs.String("record", "", "append the receipt to this card")
		keep := fs.Bool("keep-going", false, "run every step even after a failure")
		only := fs.String("only", "", "comma-separated steps")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		var steps []string
		for _, s := range strings.Split(*only, ",") {
			if s = strings.TrimSpace(s); s != "" {
				steps = append(steps, s)
			}
		}
		return Gate(root, cfg, gateOpts{record: *record, keepGoing: *keep, only: steps}, stdout)
	case "check-docs":
		return CheckDocs(root, cfg, stdout)
	case "check-boundaries":
		return CheckBoundaries(root, cfg, stdout)
	case "mutate":
		base := fs.String("base", "", "branch to compare with (default guard.base)")
		if _, err := parse(fs, args); err != nil {
			return err
		}
		return Mutate(root, cfg, *base, stdout)
	case "hook":
		if len(args) == 0 {
			return usagef("usage: keel hook pre-commit | commit-msg <file>")
		}
		switch args[0] {
		case "pre-commit":
			// Fast checks only; every one runs so a commit shows all problems at once.
			var failed []string
			for _, step := range []struct {
				name string
				fn   func() error
			}{
				{"lint", func() error { return Lint(root, cfg, nil, stdout) }},
				{"freeze-check", func() error { return freezeCheck(root, cfg, nil, true, stdout) }},
				{"check-docs", func() error { return CheckDocs(root, cfg, stdout) }},
				{"check-boundaries", func() error { return CheckBoundaries(root, cfg, stdout) }},
				{"assert-guard", func() error { return AssertGuard(root, cfg, "", stdout) }},
			} {
				if err := step.fn(); err != nil {
					fmt.Fprintf(stderr, "FAIL  %s\n", err)
					failed = append(failed, step.name)
				}
			}
			if len(failed) > 0 {
				return failf("pre-commit: %s failed", strings.Join(failed, ", "))
			}
			return nil
		case "commit-msg":
			if len(args) != 2 {
				return usagef("usage: keel hook commit-msg <file>")
			}
			return CommitMsg(args[1])
		}
		return usagef("unknown hook %q", args[0])
	}
	return usagef("unknown command; run `keel help`")
}

// RunStreaming runs a long command with live output (mutation tools).
func RunStreaming(root string, cmd []string, w io.Writer) (int, error) {
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir, c.Stdout, c.Stderr = root, w, w
	err := c.Run()
	if c.ProcessState != nil {
		return c.ProcessState.ExitCode(), nil
	}
	return -1, err
}
