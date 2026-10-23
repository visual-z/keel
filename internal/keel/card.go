package keel

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	CardTypes      = []string{"feat", "fix", "perf", "refactor", "debt", "docs", "chore", "research", "review", "release"}
	BehaviourTypes = []string{"feat", "fix", "perf"} // must carry RED evidence
	Statuses       = []string{"todo", "doing", "blocked", "awaiting-author", "done"}
	Weights        = []string{"full", "light"}
	CheckKinds     = []string{"test", "property", "inject", "cmd", "grep", "author", "measure"}
	FullSections   = []string{"来源", "范围", "观测面", "环境假设", "完成定义", "设计", "验证命令", "证据"}
	LightSections  = []string{"问题", "完成定义", "证据"}
)

// Placeholder marks template slots. `<T>` is legal code; ⟨…⟩ never is.
const Placeholder = "⟨"

const datePat = `\d{4}-\d{2}-\d{2}`

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type Check struct {
	Kind, Arg string
	Line      int
	Confirmed string // date an author confirmed a hand check
}

type Item struct {
	ID     int
	Line   int
	Lines  []string
	Checks []Check
}

var confirmedRe = regexp.MustCompile(`\s*已确认[：:]\s*` + datePat)
var spaceRe = regexp.MustCompile(`\s+`)

// FrozenText is what must not change after the freeze point. Author
// confirmations are appended later by design and are not a change.
func (it Item) FrozenText() string {
	t := confirmedRe.ReplaceAllString(strings.Join(it.Lines, "\n"), "")
	return strings.TrimSpace(spaceRe.ReplaceAllString(t, " "))
}

type RedClaim struct {
	Revision, Filter, Profile string
	Line                      int
}

type Inject struct {
	Filter, Patch, Profile string
	Line                   int
}

type Receipt struct {
	Rev, Verdict, Results string
	Line                  int
}

type Card struct {
	Rel      string
	Text     string
	Meta     map[string]string
	Sections []string
	Items    map[int]*Item
	Reds     []RedClaim
	RedNone  []struct {
		Reason string
		Line   int
	}
	Injects  []Inject
	Gates    []Receipt
	Revised  map[int]bool
	WeakenOK []string
	Errors   []string
}

var (
	dLine       = regexp.MustCompile(`^- \*\*D(\d+)\*\*\s*(.*)$`)
	checkLine   = regexp.MustCompile(`^\s+- 检[：:]\s*(\w+)\s*(.*)$`)
	redPrefix   = regexp.MustCompile(`^RED(?:\(([\w-]*)\))?:`)
	redLine     = regexp.MustCompile(`^RED(?:\(([\w-]+)\))?:\s+(\S+)\s+(\S.*?)\s*$`)
	redNoneLine = regexp.MustCompile(`^RED:\s+none\b\s*(.*)$`)
	gateLine    = regexp.MustCompile(`^GATE:\s+(\S+)\s+(ok|fail)\b(.*)$`)
	injectOpen  = regexp.MustCompile("^```diff\\s+keel-inject(?:\\(([\\w-]+)\\))?\\s+(\\S.*?)\\s*$")
	weakenLine  = regexp.MustCompile(`^WEAKEN-OK:\s+(\S+)\s+.*作者确认\s*` + datePat)
	headingLine = regexp.MustCompile(`^## (.+?)\s*$`)
	claimSuffix = regexp.MustCompile(`\s+#\s*D\d+(?:\s*,\s*D\d+)*\s*$`)
	dRef        = regexp.MustCompile(`\bD(\d+)\b`)
	metaLine    = regexp.MustCompile(`^([A-Za-z_][\w-]*)\s*:\s*(.*)$`)
	metaComment = regexp.MustCompile(`\s+#.*$`)
)

func parseFrontMatter(ls []string) (map[string]string, int) {
	meta := map[string]string{}
	if len(ls) == 0 || strings.TrimSpace(ls[0]) != "---" {
		return meta, 0
	}
	for i := 1; i < len(ls); i++ {
		if strings.TrimSpace(ls[i]) == "---" {
			return meta, i + 1
		}
		if m := metaLine.FindStringSubmatch(ls[i]); m != nil {
			v := strings.TrimSpace(metaComment.ReplaceAllString(m[2], ""))
			meta[m[1]] = strings.Trim(v, `"'`)
		}
	}
	return map[string]string{}, 0
}

// ParseCard reads a task card. Code fences and HTML comments hold examples
// and format help; nothing inside them is a claim.
func ParseCard(text, rel string) *Card {
	ls := lines(text)
	meta, start := parseFrontMatter(ls)
	c := &Card{Rel: rel, Text: text, Meta: meta, Items: map[int]*Item{}, Revised: map[int]bool{}}
	fence := "" // "", "code", "inject"
	var inj Inject
	var injBody []string
	section := ""
	var current *Item
	inComment := false
	for idx := start; idx < len(ls); idx++ {
		n, line := idx+1, ls[idx]
		if fence == "" {
			if inComment {
				inComment = !strings.Contains(line, "-->")
				continue
			}
			if strings.HasPrefix(strings.TrimLeft(line, " \t"), "<!--") {
				inComment = !strings.Contains(line, "-->")
				continue
			}
		}
		if fence != "" {
			if strings.TrimSpace(line) == "```" {
				if fence == "inject" {
					inj.Patch = strings.Join(injBody, "\n") + "\n"
					c.Injects = append(c.Injects, inj)
				}
				fence = ""
			} else if fence == "inject" {
				injBody = append(injBody, line)
			}
			continue
		}
		if m := injectOpen.FindStringSubmatch(line); m != nil {
			fence, inj, injBody = "inject", Inject{Profile: m[1], Filter: m[2], Line: n}, nil
			continue
		}
		if strings.HasPrefix(line, "```") {
			fence = "code"
			continue
		}
		if m := headingLine.FindStringSubmatch(line); m != nil {
			section = m[1]
			c.Sections = append(c.Sections, section)
			current = nil
			continue
		}
		if strings.HasPrefix(section, "完成定义") {
			if m := dLine.FindStringSubmatch(line); m != nil {
				id, _ := strconv.Atoi(m[1])
				if _, dup := c.Items[id]; dup {
					c.Errors = append(c.Errors, fmt.Sprintf("line %d: D%d is defined twice", n, id))
				}
				current = &Item{ID: id, Line: n, Lines: []string{strings.TrimSpace(line)}}
				c.Items[id] = current
				continue
			}
			if m := checkLine.FindStringSubmatch(line); m != nil && current != nil {
				ch := Check{Kind: m[1], Arg: m[2], Line: n}
				if cm := regexp.MustCompile(`已确认[：:]\s*(` + datePat + `)`).FindStringSubmatch(m[2]); cm != nil {
					ch.Confirmed = cm[1]
				}
				current.Checks = append(current.Checks, ch)
				current.Lines = append(current.Lines, strings.TrimSpace(line))
				continue
			}
			if current != nil && strings.HasPrefix(line, "  ") && strings.TrimSpace(line) != "" {
				current.Lines = append(current.Lines, strings.TrimSpace(line))
				continue
			}
			if strings.TrimSpace(line) == "" {
				continue
			}
			current = nil
		}
		if strings.HasPrefix(section, "修订") && strings.Contains(line, "作者确认") {
			for _, m := range dRef.FindAllStringSubmatch(line, -1) {
				id, _ := strconv.Atoi(m[1])
				c.Revised[id] = true
			}
		}
		// Every line that starts like a claim must parse as one. A malformed
		// claim must not quietly stop being a claim.
		if redPrefix.MatchString(line) {
			if m := redNoneLine.FindStringSubmatch(line); m != nil {
				c.RedNone = append(c.RedNone, struct {
					Reason string
					Line   int
				}{m[1], n})
				continue
			}
			m := redLine.FindStringSubmatch(line)
			if m == nil || !hexRev.MatchString(m[2]) {
				c.Errors = append(c.Errors, fmt.Sprintf("line %d: invalid RED declaration; expected `RED: <commit id> <test filter>`", n))
				continue
			}
			filter := strings.TrimSpace(claimSuffix.ReplaceAllString(m[3], ""))
			if filter == "" {
				c.Errors = append(c.Errors, fmt.Sprintf("line %d: invalid RED declaration; the filter is empty", n))
				continue
			}
			c.Reds = append(c.Reds, RedClaim{Profile: m[1], Revision: m[2], Filter: filter, Line: n})
			continue
		}
		if m := gateLine.FindStringSubmatch(line); m != nil {
			c.Gates = append(c.Gates, Receipt{Rev: m[1], Verdict: m[2], Results: m[3], Line: n})
			continue
		}
		if m := weakenLine.FindStringSubmatch(line); m != nil {
			c.WeakenOK = append(c.WeakenOK, m[1])
		}
	}
	if fence == "inject" {
		c.Errors = append(c.Errors, fmt.Sprintf("line %d: keel-inject block is not closed", inj.Line))
	}
	return c
}

// RedErrors are the malformed claim lines: what audit-red counts even in
// legacy cards, whose other format problems are exempt.
func (c *Card) RedErrors() []string {
	var out []string
	for _, e := range c.Errors {
		if strings.Contains(e, "RED declaration") {
			out = append(out, e)
		}
	}
	return out
}

func LoadCard(root, path string) (*Card, error) {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, failf("%s does not exist", path)
	}
	return ParseCard(string(b), rel(root, p)), nil
}

var resultRe = regexp.MustCompile(`(?:^|\s)([\w.-]+)=(\d+)`)

// ReceiptCovers: a receipt proves completion only if every configured step
// ran and exited 0. A partial or hand-trimmed receipt that says ok must not.
func ReceiptCovers(results string, steps []string) bool {
	found := map[string]string{}
	for _, m := range resultRe.FindAllStringSubmatch(results, -1) {
		found[m[1]] = m[2]
	}
	for _, s := range steps {
		if found[s] != "0" {
			return false
		}
	}
	return true
}

var nameRe = map[string]*regexp.Regexp{}

func LintCard(c *Card, requiredSteps []string) []string {
	probs := append([]string{}, c.Errors...)
	m := c.Meta
	if len(m) == 0 {
		return append(probs, "missing front matter (--- type/status/weight/created ---)")
	}
	for _, k := range []string{"type", "status", "weight", "created"} {
		if m[k] == "" {
			probs = append(probs, fmt.Sprintf("front matter is missing `%s`", k))
		}
	}
	typ, status, weight := m["type"], m["status"], m["weight"]
	if typ != "" && !contains(CardTypes, typ) {
		probs = append(probs, fmt.Sprintf("type `%s` is not one of %s", typ, strings.Join(CardTypes, ", ")))
	}
	if status != "" && !contains(Statuses, status) {
		probs = append(probs, fmt.Sprintf("status `%s` is not one of %s", status, strings.Join(Statuses, ", ")))
	}
	if weight != "" && !contains(Weights, weight) {
		probs = append(probs, fmt.Sprintf("weight `%s` is not one of %s", weight, strings.Join(Weights, ", ")))
	}
	if contains(CardTypes, typ) {
		re, ok := nameRe[typ]
		if !ok {
			re = regexp.MustCompile(`^` + typ + `-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)
			nameRe[typ] = re
		}
		if !re.MatchString(filepath.Base(c.Rel)) {
			probs = append(probs, fmt.Sprintf("file name must be `%s-<kebab-slug>.md`", typ))
		}
	}
	if status == "blocked" && m["reason"] == "" {
		probs = append(probs, "a blocked card needs `reason:` in front matter")
	}
	required := LightSections
	if weight == "full" {
		required = FullSections
	}
	for _, sec := range required {
		found := false
		for _, s := range c.Sections {
			if strings.HasPrefix(s, sec) {
				found = true
				break
			}
		}
		if !found {
			probs = append(probs, fmt.Sprintf("missing section `## %s`", sec))
		}
	}
	if len(c.Items) == 0 {
		probs = append(probs, "完成定义 has no `- **D1** …` items")
	}
	ids := make([]int, 0, len(c.Items))
	for id := range c.Items {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		it := c.Items[id]
		if len(it.Checks) == 0 {
			probs = append(probs, fmt.Sprintf("D%d has no `- 检：<kind> …` line: a criterion nothing checks is a wish", id))
		}
		for _, ch := range it.Checks {
			if !contains(CheckKinds, ch.Kind) {
				probs = append(probs, fmt.Sprintf("line %d: check kind `%s` is not one of %s", ch.Line, ch.Kind, strings.Join(CheckKinds, ", ")))
			}
			if contains([]string{"test", "property", "grep", "cmd"}, ch.Kind) && !strings.Contains(ch.Arg, "`") {
				probs = append(probs, fmt.Sprintf("line %d: `%s` check must name its target in backticks", ch.Line, ch.Kind))
			}
		}
	}
	started := contains([]string{"doing", "awaiting-author", "done", "blocked"}, status)
	if started && strings.Contains(c.Text, Placeholder) {
		probs = append(probs, "template placeholders ⟨…⟩ are still present")
	}
	behaviour := contains(BehaviourTypes, typ)
	if len(c.RedNone) > 0 && behaviour {
		probs = append(probs, fmt.Sprintf("`RED: none` is not allowed on a `%s` card: behaviour changes need a red test", typ))
	}
	for _, rn := range c.RedNone {
		if len(strings.Trim(rn.Reason, " —-")) < 4 {
			probs = append(probs, fmt.Sprintf("line %d: `RED: none` needs a reason", rn.Line))
		}
	}
	finished := status == "awaiting-author" || status == "done"
	if finished && behaviour && len(c.Reds) == 0 && len(c.Injects) == 0 {
		probs = append(probs, fmt.Sprintf("a finished `%s` card needs a `RED: <commit> <filter>` line or a keel-inject block", typ))
	}
	if status == "done" {
		var oks []string
		for _, g := range c.Gates {
			if g.Verdict == "ok" {
				oks = append(oks, g.Results)
			}
		}
		if len(oks) == 0 {
			probs = append(probs, "a done card needs a `GATE: <rev> ok …` receipt (keel gate --record <card>)")
		} else if len(requiredSteps) > 0 {
			covered := false
			for _, r := range oks {
				if ReceiptCovers(r, requiredSteps) {
					covered = true
					break
				}
			}
			if !covered {
				probs = append(probs, fmt.Sprintf("the GATE receipt does not show every gate step exiting 0 (%s); a partial gate is not a completion receipt", strings.Join(requiredSteps, ", ")))
			}
		}
		for _, id := range ids {
			for _, ch := range c.Items[id].Checks {
				if ch.Kind == "author" && ch.Confirmed == "" {
					probs = append(probs, fmt.Sprintf("D%d: author check is not confirmed (`已确认：YYYY-MM-DD`); use status awaiting-author until the author confirms", id))
				}
			}
		}
	}
	return probs
}

// ---- the card set and the legacy manifest

func AllCardPaths(root string, cfg *Config) []string {
	dir := filepath.Join(root, cfg.Paths.Tasks)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".md") || strings.HasPrefix(n, "TEMPLATE") || strings.HasPrefix(n, "README") {
			continue
		}
		out = append(out, filepath.Join(dir, n))
	}
	sort.Strings(out)
	return out
}

func ReadManifest(text string) []string {
	var out []string
	for _, l := range lines(text) {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// LegacyNames: cards that predate keel in an adopted project. They keep
// their old format; every card not listed must comply; the list only shrinks.
func LegacyNames(root string, cfg *Config) []string {
	return ReadManifest(readFile(filepath.Join(root, cfg.Paths.LegacyManifest)))
}

func CardPaths(root string, cfg *Config) []string {
	legacy := map[string]bool{}
	for _, n := range LegacyNames(root, cfg) {
		legacy[n] = true
	}
	var out []string
	for _, p := range AllCardPaths(root, cfg) {
		if !legacy[filepath.Base(p)] {
			out = append(out, p)
		}
	}
	return out
}
