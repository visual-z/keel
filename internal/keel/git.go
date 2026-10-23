package keel

import (
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Live throwaway worktrees. An interrupt must not leave them registered in
// `git worktree list`, so a signal handler removes them before exiting.
var (
	liveMu    sync.Mutex
	live      = map[string]*Worktree{}
	sigOnce   sync.Once
	sigExitFn = os.Exit
)

type Worktree struct {
	Root, Path string
	links      []string
}

func installSignalCleanup() {
	sigOnce.Do(func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-ch
			liveMu.Lock()
			for _, w := range live {
				w.remove()
			}
			liveMu.Unlock()
			sigExitFn(130)
		}()
	})
}

// NewWorktree checks rev out into a temporary directory and links the
// configured dependency directories (a fresh checkout has no node_modules;
// load errors from missing deps must not be read as red).
func NewWorktree(root, rev string, links []string) (*Worktree, error) {
	installSignalCleanup()
	dir, err := os.MkdirTemp("", "keel-red-")
	if err != nil {
		return nil, err
	}
	if _, out, ok := GitIn(root, "", nil, "worktree", "add", "--detach", "--quiet", dir, rev); !ok {
		os.RemoveAll(dir)
		return nil, failf("could not create a worktree at %s: %s", rev, out)
	}
	w := &Worktree{Root: root, Path: dir}
	for _, name := range links {
		src, dst := filepath.Join(root, name), filepath.Join(dir, name)
		if exists(src) && !exists(dst) {
			_ = os.MkdirAll(filepath.Dir(dst), 0o755)
			if os.Symlink(src, dst) == nil {
				w.links = append(w.links, dst)
			}
		}
	}
	liveMu.Lock()
	live[dir] = w
	liveMu.Unlock()
	return w, nil
}

func (w *Worktree) remove() {
	// Unlink first so a recursive delete can never walk into the real
	// dependency directory through the link.
	for _, l := range w.links {
		if fi, err := os.Lstat(l); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			os.Remove(l)
		}
	}
	GitIn(w.Root, "", nil, "worktree", "remove", "--force", w.Path)
	os.RemoveAll(w.Path)
	GitIn(w.Root, "", nil, "worktree", "prune")
}

func (w *Worktree) Close() {
	liveMu.Lock()
	delete(live, w.Path)
	liveMu.Unlock()
	w.remove()
}

// ---- retention: a documented object id is not a git retention root

type RefState int

const (
	RefMissing RefState = iota
	RefRetained
	RefConflict
)

func RefStateOf(root, prefix, sha string) RefState {
	ref := prefix + sha
	if _, ok := Git(root, "symbolic-ref", "--quiet", ref); ok {
		return RefConflict
	}
	out, ok := Git(root, "rev-parse", "--verify", "--quiet", ref)
	if !ok {
		return RefMissing
	}
	if out == sha {
		return RefRetained
	}
	return RefConflict
}

// Retain pins a RED commit so a squash or an expired reflog cannot erase the
// evidence. A create transaction refuses existing refs (including dangling
// symbolic ones); keel never moves a pointer to make a check green.
func Retain(root, prefix, sha string) error {
	switch RefStateOf(root, prefix, sha) {
	case RefRetained:
		return nil
	case RefConflict:
		return failf("%s%s exists and points elsewhere; refusing to move it", prefix, sha)
	}
	GitIn(root, "option no-deref\ncreate "+prefix+sha+" "+sha+"\n", nil, "update-ref", "--stdin")
	if RefStateOf(root, prefix, sha) != RefRetained {
		return failf("could not retain %s without overwriting a reference", sha)
	}
	return nil
}

// MergeBase finds where this branch left base (local first, then origin).
func MergeBase(root, base string) (string, error) {
	for _, ref := range []string{base, "origin/" + base} {
		if out, ok := Git(root, "merge-base", ref, "HEAD"); ok {
			return out, nil
		}
	}
	return "", failf("cannot find a merge base with `%s` (set guard.base or pass --base)", base)
}

// FirstCommitted returns the commit that first added rel at this path, and
// its text then. It does not use --follow: rename detection is a similarity
// heuristic and pairs a card with any identical file, which yields a "freeze
// point" where the card never existed. Renaming a card therefore moves its
// freeze point to the rename commit. ok=false means never committed; an
// error means the history exists but cannot be read (fail closed).
func FirstCommitted(root, rel string) (sha, text string, ok bool, err error) {
	out, good := Git(root, "log", "--diff-filter=A", "--format=%H", "--", rel)
	if !good || out == "" {
		return "", "", false, nil
	}
	shas := strings.Fields(out)
	sha = shas[len(shas)-1]
	text, good = gitShow(root, sha, rel)
	if !good {
		return sha, "", true, failf("%s: cannot read it at its freeze commit %s", rel, sha[:12])
	}
	return sha, text, true, nil
}

func gitShow(root, sha, path string) (string, bool) {
	r, err := Run(root, nil, 0, "", "git", "show", sha+":"+path)
	if err != nil || r.Code != 0 {
		return "", false
	}
	return r.Stdout, true
}

func HeadLabel(root string) string {
	sha, ok := Git(root, "rev-parse", "--short=12", "HEAD")
	if !ok {
		sha = "unborn"
	}
	if st, _ := Git(root, "status", "--porcelain"); st != "" {
		sha += "+dirty"
	}
	return sha
}
