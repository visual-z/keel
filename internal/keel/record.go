package keel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Gate records: a receipt in a card is text anyone can type. A record is a
// commit under refs/keel/gate/ whose tree is exactly what the gate tested and
// whose message is the receipt, the way git-test keys results by tree. A card
// that becomes done must carry a receipt with a record, and the record's tree
// must still be the tree being closed: code edited after the gate makes the
// receipt stale. Cards already done in history are not re-judged.
const gateRefPrefix = "refs/keel/gate/"

var receiptTree = regexp.MustCompile(`(?:^|\s)tree:([0-9a-f]{12,64})\b`)

// unbound: the paths a receipt does not cover. By default markdown outside
// the test paths (cards, closing notes, maps), which the pre-commit hook
// checks on its own, and keel's own scratch directory always.
func unbound(cfg *Config, path string) bool {
	if strings.HasPrefix(path, ".keel/") {
		return true
	}
	pats := cfg.Gate.Unbound
	if pats == nil {
		pats = []string{"**/*.md"}
	}
	scope := append(append([]string{}, cfg.Tests.Paths...), cfg.Tests.InlinePaths...)
	return GlobMatch(path, pats) && !GlobMatch(path, scope)
}

// BoundTree is the tree a receipt is bound to: the working tree (or, in the
// pre-commit hook, the index being committed) without its unbound paths.
func BoundTree(root string, cfg *Config, fromIndex bool) (string, error) {
	idx, done, err := tempIndex(root)
	if err != nil {
		return "", err
	}
	defer done()
	env := append(os.Environ(), "GIT_INDEX_FILE="+idx)
	if fromIndex {
		real, _ := Git(root, "rev-parse", "--git-path", "index")
		if !filepath.IsAbs(real) {
			real = filepath.Join(root, real)
		}
		b, err := os.ReadFile(real)
		if err != nil {
			return "", failf("cannot read the index: %v", err)
		}
		if err := os.WriteFile(idx, b, 0o644); err != nil {
			return "", err
		}
	} else {
		if _, ok := ResolveCommit(root, "HEAD"); ok {
			if _, msg, ok := GitIn(root, "", env, "read-tree", "HEAD"); !ok {
				return "", failf("git read-tree failed: %s", msg)
			}
		}
		if _, msg, ok := GitIn(root, "", env, "add", "-A", "--", "."); !ok {
			return "", failf("git add failed: %s", msg)
		}
	}
	listed, _, _ := GitIn(root, "", env, "-c", "core.quotePath=false", "ls-files")
	var drop []string
	for _, p := range lines(listed) {
		if p != "" && unbound(cfg, p) {
			drop = append(drop, p)
		}
	}
	if len(drop) > 0 {
		if _, msg, ok := GitIn(root, strings.Join(drop, "\x00")+"\x00", env, "update-index", "--force-remove", "-z", "--stdin"); !ok {
			return "", failf("git update-index failed: %s", msg)
		}
	}
	tree, msg, ok := GitIn(root, "", env, "write-tree")
	if !ok {
		return "", failf("git write-tree failed: %s", msg)
	}
	return tree, nil
}

// WriteRecord stores a gate record: a commit of the tested tree (parent
// HEAD) whose message holds the receipt and anything a reader of the
// receipt must also know (flakes, failures the anchor shares).
func WriteRecord(root, tree, receipt string, notes []string) error {
	msg := "keel gate record\n\n" + receipt + "\n"
	if len(notes) > 0 {
		msg += "\n" + strings.Join(notes, "\n") + "\n"
	}
	args := []string{"commit-tree", tree, "-m", msg}
	if head, ok := ResolveCommit(root, "HEAD"); ok {
		args = append(args, "-p", head)
	}
	sha, out, ok := GitIn(root, "", nil, args...)
	if !ok {
		return failf("cannot write the gate record: %s", out)
	}
	if _, out, ok := GitIn(root, "create "+gateRefPrefix+sha+" "+sha+"\n", nil, "update-ref", "--stdin"); !ok {
		return failf("cannot retain the gate record: %s", out)
	}
	return nil
}

// findRecord returns the tree of the record holding exactly this receipt.
func findRecord(root, receipt string) (tree string, ok bool) {
	m := receiptTree.FindStringSubmatch(receipt)
	if m == nil {
		return "", false
	}
	out, _ := Git(root, "for-each-ref", "--format=%(objectname) %(tree)", gateRefPrefix)
	for _, l := range lines(out) {
		f := strings.Fields(l)
		if len(f) != 2 || !strings.HasPrefix(f[1], m[1]) {
			continue
		}
		body, _ := Git(root, "log", "-1", "--format=%B", f[0])
		for _, bl := range lines(body) {
			if strings.TrimSpace(bl) == strings.TrimSpace(receipt) {
				return f[1], true
			}
		}
	}
	return "", false
}

// closingProblems judges the cards that become done now: done in the
// working tree (or the index, in the hook) but not done at HEAD.
func closingProblems(root string, cfg *Config, card *Card, staged bool) []string {
	if card.Meta["status"] != "done" {
		return nil
	}
	if old, ok := gitShow(root, "HEAD", card.Rel); ok && ParseCard(old, card.Rel).Meta["status"] == "done" {
		return nil
	}
	var last *Receipt
	for i := range card.Gates {
		if card.Gates[i].Verdict == "ok" {
			last = &card.Gates[i]
		}
	}
	if last == nil {
		return nil // LintCard reports the missing receipt
	}
	line := "GATE: " + last.Rev + " ok" + last.Results
	if !receiptTree.MatchString(last.Results) {
		return []string{"the GATE receipt is not bound to a tested tree (hand-written, or from keel < 1.1); re-run `keel gate --record " + card.Rel + "`"}
	}
	tree, ok := findRecord(root, line)
	if !ok {
		return []string{"the GATE receipt has no record under " + gateRefPrefix + " in this repository: only `keel gate --record` writes one"}
	}
	now, err := BoundTree(root, cfg, staged)
	if err != nil {
		return []string{err.Error()}
	}
	if now != tree {
		// A file that was lying around untracked while the gate ran and is
		// not part of what is closed now (build output, a scratch file) is
		// not a change to the tested code; everything else is.
		out, _ := Git(root, "-c", "core.quotePath=false", "diff", "--name-status", "--no-renames", tree, now)
		var files []string
		for _, l := range lines(out) {
			f := strings.SplitN(l, "\t", 2)
			if len(f) != 2 {
				continue
			}
			if _, tracked := Git(root, "cat-file", "-e", "HEAD:"+f[1]); f[0] == "D" && !tracked {
				continue
			}
			files = append(files, f[1])
		}
		if len(files) == 0 {
			return nil
		}
		more := ""
		if len(files) > 5 {
			files, more = files[:5], " …"
		}
		return []string{fmt.Sprintf("the GATE receipt is stale: %s changed after the gate ran; re-run `keel gate --record %s`", strings.Join(files, ", ")+more, card.Rel)}
	}
	return nil
}
