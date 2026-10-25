package keel

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

// Injected faults. A keel-inject patch is written against the tree of its
// day. Later the code moves and is rewritten, and a context patch stops
// applying. Instead of rewriting the patch (paths, line numbers), keel makes
// the fault a commit on a tree it applies to, and carries it to the target
// with git's three-way merge, which follows renames and shifted context the
// way a cherry-pick does. A fault whose code was truly rewritten conflicts,
// and is reported as such rather than guessed at.
const faultRefPrefix = "refs/keel/fault/"

// faultSearchDepth bounds the walk back for a commit a patch applies to.
const faultSearchDepth = 500

type faultPlan struct {
	commit  string   // the target with the fault in it
	source  string   // the fault as first applied (retained for replays)
	how     string   // how it got onto the target
	touched []string // paths it changes on the target
}

func faultKey(inj Inject) string {
	h := sha1.Sum([]byte(inj.Profile + "\x00" + inj.Filter + "\x00" + inj.Patch))
	return hex.EncodeToString(h[:])
}

// patchCommit applies a patch to base in a private index (no checkout) and
// commits the result on top of base.
func patchCommit(root, base, patch string) (string, error) {
	idx, done, err := tempIndex(root)
	if err != nil {
		return "", err
	}
	defer done()
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	if _, msg, ok := GitIn(root, "", env, "read-tree", base); !ok {
		return "", failf("git read-tree failed: %s", msg)
	}
	// --unidiff-zero: a one-line fault is naturally written without context.
	if _, msg, ok := GitIn(root, patch, env, "apply", "--cached", "--unidiff-zero", "--whitespace=nowarn", "-"); !ok {
		return "", failf("%s", msg)
	}
	tree, msg, ok := GitIn(root, "", env, "write-tree")
	if !ok {
		return "", failf("git write-tree failed: %s", msg)
	}
	sha, msg, ok := GitIn(root, "", nil, "commit-tree", tree, "-p", base, "-m", "keel fault")
	if !ok {
		return "", failf("commit-tree failed: %s", msg)
	}
	return sha, nil
}

// planFault puts inj onto target: directly when the patch applies there;
// otherwise from the fault retained by an earlier verification, or from the
// newest ancestor the patch applies to, merged onto target.
func planFault(root string, inj Inject, target string) (faultPlan, error) {
	if len(PatchPaths(inj.Patch)) == 0 {
		return faultPlan{}, failf("keel-inject block is not a unified diff")
	}
	direct, derr := patchCommit(root, target, inj.Patch)
	if derr == nil {
		return finishPlan(root, faultPlan{commit: direct, source: direct, how: "applied to " + target[:12]}, target)
	}
	source := ""
	if sha, ok := Git(root, "rev-parse", "--verify", "--quiet", faultRefPrefix+faultKey(inj)+"^{commit}"); ok {
		source = sha
	} else {
		revs, _ := Git(root, "rev-list", "--first-parent", "-n", fmt.Sprint(faultSearchDepth), target)
		for _, rev := range strings.Fields(revs)[1:] {
			if sha, err := patchCommit(root, rev, inj.Patch); err == nil {
				source = sha
				break
			}
		}
	}
	if source == "" {
		return faultPlan{}, failf("the fault patch does not apply to %s, nor to any of its last %d first-parent ancestors: %s", target[:12], faultSearchDepth, derr)
	}
	parent, _ := Git(root, "rev-parse", source+"^")
	out, ok := Git(root, "merge-tree", "--write-tree", "--no-messages", "--merge-base="+parent, target, source)
	if !ok {
		conflicts := lines(out)[1:]
		return faultPlan{}, failf("the fault was written against %s and no longer merges onto %s: the code it breaks has been rewritten (%s)", parent[:12], target[:12], strings.Join(conflicts, ", "))
	}
	tree := lines(out)[0]
	commit, msg, ok := GitIn(root, "", nil, "commit-tree", tree, "-p", target, "-m", "keel fault (merged)")
	if !ok {
		return faultPlan{}, failf("commit-tree failed: %s", msg)
	}
	return finishPlan(root, faultPlan{commit: commit, source: source, how: "merged from " + parent[:12]}, target)
}

func finishPlan(root string, p faultPlan, target string) (faultPlan, error) {
	out, _ := Git(root, "-c", "core.quotePath=false", "diff", "--name-only", "--no-renames", target, p.commit)
	p.touched = strings.Fields(out)
	if len(p.touched) == 0 {
		return p, failf("the fault patch changes nothing on %s (that code already reads as the fault)", target[:12])
	}
	return p, nil
}

// retainFault keeps the fault as first applied, so a later replay merges
// it from there. An existing ref is an earlier origin and stays.
func retainFault(root string, inj Inject, source string) {
	GitIn(root, "option no-deref\ncreate "+faultRefPrefix+faultKey(inj)+" "+source+"\n", nil, "update-ref", "--stdin")
}

type replayOpts struct {
	since  string
	strict bool
}

// Replay re-applies every injected fault of every card (or of the cards
// changed since a revision) to HEAD and runs its tests: the evidence still
// stands only if they still go red. A fault the tests no longer catch fails
// the replay. One whose code has since been rewritten cannot be replayed;
// it is listed, and fails only with --strict, because it was verified on
// its own day and nothing here says it is wrong.
func Replay(root string, cfg *Config, cards []string, o replayOpts, w io.Writer) error {
	head, ok := ResolveCommit(root, "HEAD")
	if !ok {
		return failf("replay needs a commit")
	}
	paths := cards
	if len(paths) == 0 {
		paths = AllCardPaths(root, cfg)
	}
	if o.since != "" {
		since, ok := ResolveCommit(root, o.since)
		if !ok {
			if s, ok2 := Git(root, "rev-parse", "--verify", "--quiet", o.since+"^{commit}"); ok2 {
				since = s
			} else {
				return failf("cannot resolve %s", o.since)
			}
		}
		changed, _ := Git(root, "-c", "core.quotePath=false", "diff", "--name-only", since, "--", cfg.Paths.Tasks)
		set := map[string]bool{}
		for _, f := range strings.Fields(changed) {
			set[f] = true
		}
		var keep []string
		for _, p := range paths {
			if set[rel(root, absPath(root, p))] {
				keep = append(keep, p)
			}
		}
		paths = keep
	}
	var red, decayed, skipped, abnormal int
	for _, path := range paths {
		card, err := LoadCard(root, path)
		if err != nil {
			return err
		}
		for _, inj := range card.Injects {
			where := fmt.Sprintf("%s:%d %s", card.Rel, inj.Line, inj.Filter)
			plan, err := planFault(root, inj, head)
			if err != nil {
				skipped++
				fmt.Fprintf(w, "skip    %s\n        %v\n", where, err)
				continue
			}
			if bad := testPaths(cfg, plan.touched); len(bad) > 0 {
				abnormal++
				fmt.Fprintf(w, "refuse  %s: the fault edits tests (%s)\n", where, strings.Join(bad, ", "))
				continue
			}
			rc, err := cfg.RedFor(inj.Profile)
			if err != nil {
				return err
			}
			wt, err := NewWorktree(root, plan.commit, rc)
			if err != nil {
				return err
			}
			out, err := RunFilter(root, rc, wt.Path, inj.Filter)
			wt.Close()
			if err != nil {
				return err
			}
			rerr := RequireRed(rc, out, where)
			switch {
			case rerr == nil:
				red++
				fmt.Fprintf(w, "red     %s (%s)\n", where, plan.how)
			case out.Res.Code == 0 && out.Sum.Invalid == "" && out.Sum.Selected > 0:
				decayed++
				fmt.Fprintf(w, "GREEN   %s (%s): the tests no longer catch this fault\n", where, plan.how)
			default:
				abnormal++
				fmt.Fprintf(w, "?       %s (%s): not a clean red\n        %s\n", where, plan.how, strings.SplitN(rerr.Error(), "\n", 2)[0])
			}
		}
	}
	fmt.Fprintf(w, "replay: %d fault(s) still red, %d no longer caught, %d cannot be replayed, %d abnormal\n", red, decayed, skipped, abnormal)
	if decayed > 0 || (o.strict && skipped+abnormal > 0) {
		return failf("replay failed")
	}
	return nil
}

func testPaths(cfg *Config, paths []string) []string {
	var bad []string
	for _, p := range paths {
		if GlobMatch(p, cfg.Tests.Paths) {
			bad = append(bad, p)
		}
	}
	return bad
}

func absPath(root, p string) string {
	if strings.HasPrefix(p, "/") {
		return p
	}
	return root + "/" + p
}
