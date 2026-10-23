package keel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Fail is a check that did not pass. It prints as FAIL and exits 1.
type Fail struct{ Msg string }

func (f *Fail) Error() string { return f.Msg }

func failf(format string, a ...any) error { return &Fail{fmt.Sprintf(format, a...)} }

// Result of one child process.
type Result struct {
	Code     int  // exit code; -1 when killed by a signal or timed out
	Signaled bool // killed by a signal (a crash is not a red)
	TimedOut bool
	Out      string // stdout + stderr
	Stdout   string
}

// Run executes a command in its own process group, so a timeout kills the
// whole tree (cargo spawns test binaries; killing only cargo leaves them).
func Run(dir string, env []string, timeout time.Duration, input string, name string, args ...string) (Result, error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	// stdout and stderr are copied by two goroutines; the shared combined
	// buffer must be locked, or interleaved writes are silently lost.
	var stdout, combined bytes.Buffer
	var mu sync.Mutex
	cmd.Stdout = &lockedWriter{mu: &mu, bufs: []*bytes.Buffer{&stdout, &combined}}
	cmd.Stderr = &lockedWriter{mu: &mu, bufs: []*bytes.Buffer{&combined}}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	timedOut := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		timedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-done
	}
	res := Result{Out: combined.String(), Stdout: stdout.String(), TimedOut: timedOut}
	if timedOut {
		res.Code = -1
		return res, nil
	}
	if cmd.ProcessState != nil {
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			res.Signaled = true
			res.Code = -1
			return res, nil
		}
		res.Code = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return res, waitErr
	}
	return res, nil
}

type lockedWriter struct {
	mu   *sync.Mutex
	bufs []*bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range w.bufs {
		b.Write(p)
	}
	return len(p), nil
}

// Git runs git in root. Output is trimmed.
func Git(root string, args ...string) (string, bool) {
	r, err := Run(root, nil, 0, "", "git", args...)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(r.Stdout), r.Code == 0
}

func GitIn(root, input string, env []string, args ...string) (string, string, bool) {
	r, err := Run(root, env, 0, input, "git", args...)
	if err != nil {
		return "", err.Error(), false
	}
	return strings.TrimSpace(r.Stdout), strings.TrimSpace(r.Out), r.Code == 0
}

var hexRev = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
var fullSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ResolveCommit turns an abbreviated commit id (or HEAD) into a full sha.
// Branch and tag names are refused: a claim must name an immutable commit.
func ResolveCommit(root, rev string) (string, bool) {
	if rev != "HEAD" && !hexRev.MatchString(rev) {
		return "", false
	}
	out, ok := Git(root, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if !ok || !fullSHA.MatchString(out) {
		return "", false
	}
	return out, true
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

func stripANSI(s string) string { return ansi.ReplaceAllString(s, "") }

// globRegexp compiles a path glob. `*` crosses directories (fnmatch
// semantics, which every keel.toml was written against); `**/` also matches
// zero leading directories.
func globRegexp(pat string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pat); i++ {
		c := pat[i]
		switch {
		case strings.HasPrefix(pat[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case c == '*':
			b.WriteString(".*")
		case c == '?':
			b.WriteString(".")
		case c == '[':
			j := strings.IndexByte(pat[i:], ']')
			if j > 0 {
				b.WriteString(pat[i : i+j+1])
				i += j
			} else {
				b.WriteString(`\[`)
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

func GlobMatch(path string, patterns []string) bool {
	for _, p := range patterns {
		if globRegexp(p).MatchString(path) {
			return true
		}
	}
	return false
}

// TrackedFiles lists tracked and untracked-but-not-ignored files.
func TrackedFiles(root string) []string {
	out, ok := Git(root, "ls-files", "--cached", "--others", "--exclude-standard")
	if !ok {
		return nil
	}
	var files []string
	for _, l := range strings.Split(out, "\n") {
		if l != "" {
			if _, err := os.Stat(filepath.Join(root, l)); err == nil {
				files = append(files, l)
			}
		}
	}
	return files
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func rel(root, path string) string {
	r, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(r)
}

func lines(s string) []string { return strings.Split(s, "\n") }
