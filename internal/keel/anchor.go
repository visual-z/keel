package keel

import (
	"strings"
)

// CardAnchor is the commit a card's work is measured from: where the card
// was opened (its freeze commit), or the `baseline:` it names when that is
// earlier. Only an earlier baseline counts: declaring one can widen what the
// guard compares, never narrow it. ok=false: the card was never committed
// and names no usable baseline.
//
// The anchor does not depend on the branch: on trunk the merge base with the
// base branch is HEAD itself, and a diff against it sees nothing committed.
func CardAnchor(root string, card *Card) (sha, how string, ok bool, err error) {
	freeze, _, frozen, err := FirstCommitted(root, card.Rel)
	if err != nil {
		return "", "", false, err
	}
	base, hasBase := "", false
	if f := strings.FieldsFunc(card.Meta["baseline"], func(r rune) bool { return r == ';' || r == ' ' || r == '；' || r == ',' }); len(f) > 0 {
		base, hasBase = ResolveCommit(root, f[0])
	}
	switch {
	case frozen && hasBase && base != freeze && isAncestor(root, base, freeze):
		return base, "baseline " + base[:12] + " (before the freeze commit)", true, nil
	case frozen:
		return freeze, "freeze commit " + freeze[:12], true, nil
	case hasBase && isAncestor(root, base, "HEAD"):
		return base, "baseline " + base[:12], true, nil
	}
	return "", "", false, nil
}

func isAncestor(root, a, b string) bool {
	_, ok := Git(root, "merge-base", "--is-ancestor", a, b)
	return ok
}

// guardBase picks the commit assert-guard compares with: the card's anchor
// when a card is named, else the merge base with the base branch.
func guardBase(root string, cfg *Config, base, cardPath string) (sha, label string, err error) {
	if cardPath != "" {
		card, err := LoadCard(root, cardPath)
		if err != nil {
			return "", "", err
		}
		sha, how, ok, err := CardAnchor(root, card)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", failf("%s has no anchor: commit the card (its freeze point) or give it a `baseline:` commit", card.Rel)
		}
		return sha, how + " of " + card.Rel, nil
	}
	if base == "" {
		base = cfg.Guard.Base
	}
	mb, err := MergeBase(root, base)
	if err != nil {
		return "", "", err
	}
	return mb, "merge base with " + base, nil
}
