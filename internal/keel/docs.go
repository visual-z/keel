package keel

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	mdLink     = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	backtick   = regexp.MustCompile("`([^`\\s]+)`")
	inlineCode = regexp.MustCompile("`[^`\\n]*`")
	fencedCode = regexp.MustCompile("(?s)```.*?```")
	schemeRe   = regexp.MustCompile(`^[a-z]+:|^#|^mailto:`)
	mapSkip    = regexp.MustCompile(`[<>⟨⟩*{}$:|]`)
	ruleRe     = regexp.MustCompile(`(?m)^- \*\*`)
	decisionRe = regexp.MustCompile(`(?m)^\|\s*(\d+)\s*\|`)
	progressRe = regexp.MustCompile(`(?m)^## (` + datePat + `)`)
	tableSep   = regexp.MustCompile(`^\|[\s:|-]+\|$`)
	invID      = regexp.MustCompile(`^INV-\d+$`)
	fileExt    = regexp.MustCompile(`\.\w+$`)
	goPattern  = regexp.MustCompile(`/\.\.\.$`)
)

// mapPathExists: a map names a path, optionally followed by a symbol
// (`pkg/file.Func`, `dir/mod.rs::item`) or a Go package pattern (`pkg/...`).
func mapPathExists(root, tok string) bool {
	path := strings.SplitN(strings.SplitN(tok, "#", 2)[0], "::", 2)[0]
	path = strings.TrimRight(goPattern.ReplaceAllString(path, ""), "/")
	for path != "" {
		if exists(filepath.Join(root, path)) {
			return true
		}
		dir, last := filepath.Split(path)
		dot := strings.LastIndex(last, ".")
		if dot < 0 {
			return false
		}
		path = dir + last[:dot]
	}
	return false
}

// gitIgnored: build outputs (dist/, node_modules/) are legitimately named by
// maps but absent from a fresh checkout. `dist/` patterns only match
// directories, and an absent path is not known to be one, so ask both ways.
func gitIgnored(root, path string) bool {
	path = strings.TrimRight(path, "/")
	for _, p := range []string{path, path + "/"} {
		if _, ok := Git(root, "check-ignore", "-q", "--no-index", "--", p); ok {
			return true
		}
	}
	return false
}

// sectionText is the body under the first heading containing `heading`, up
// to the next heading of the same or a higher level.
func sectionText(text, heading string) (string, bool) {
	ls := lines(text)
	head := regexp.MustCompile(`^(#{1,6}) (.*)$`)
	for i, l := range ls {
		m := head.FindStringSubmatch(l)
		if m == nil || !strings.Contains(m[2], heading) {
			continue
		}
		level := len(m[1])
		var body []string
		for _, n := range ls[i+1:] {
			if nm := regexp.MustCompile(`^(#{1,6}) `).FindStringSubmatch(n); nm != nil && len(nm[1]) <= level {
				break
			}
			body = append(body, n)
		}
		return strings.Join(body, "\n"), true
	}
	return "", false
}

func tableRows(text string) [][]string {
	var rows [][]string
	for _, l := range lines(text) {
		if strings.HasPrefix(l, "|") && !tableSep.MatchString(strings.TrimSpace(l)) {
			cells := strings.Split(strings.Trim(strings.TrimSpace(l), "|"), "|")
			for i := range cells {
				cells[i] = strings.TrimSpace(cells[i])
			}
			rows = append(rows, cells)
		}
	}
	return rows
}

func symbolExists(root string, files []string, sym string) bool {
	for _, f := range files {
		if strings.HasSuffix(f, ".md") {
			continue
		}
		if strings.Contains(readFile(filepath.Join(root, f)), sym) {
			return true
		}
	}
	return false
}

// checkRegistry: registries (invariants, fixtures) point at code. A pointer
// to nothing is a claim with no checker behind it.
func checkRegistry(root, relPath string, requireInvID bool, files []string) []string {
	text := readFile(filepath.Join(root, relPath))
	rows := tableRows(text)
	if len(rows) < 2 {
		return nil
	}
	loc := -1
	for i, h := range rows[0] {
		if strings.Contains(h, "位置") {
			loc = i
		}
	}
	var probs []string
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) == 0 || row[0] == "" || strings.Contains(strings.Join(row, ""), Placeholder) {
			continue
		}
		key := row[0]
		if requireInvID && !invID.MatchString(key) {
			probs = append(probs, fmt.Sprintf("%s: id `%s` must look like INV-<n>", relPath, key))
		}
		if seen[key] {
			probs = append(probs, fmt.Sprintf("%s: `%s` is listed twice", relPath, key))
		}
		seen[key] = true
		if strings.Contains(strings.Join(row, " "), "计划") || loc < 0 || loc >= len(row) {
			continue
		}
		for _, m := range backtick.FindAllStringSubmatch(row[loc], -1) {
			tok := m[1]
			target := strings.SplitN(strings.SplitN(tok, "::", 2)[0], "#", 2)[0]
			if strings.Contains(target, "/") || fileExt.MatchString(target) {
				if !exists(filepath.Join(root, target)) {
					probs = append(probs, fmt.Sprintf("%s: `%s` points at missing path `%s`", relPath, key, tok))
				}
			} else if !symbolExists(root, files, tok) {
				probs = append(probs, fmt.Sprintf("%s: `%s` points at `%s`, which appears in no source file", relPath, key, tok))
			}
		}
	}
	return probs
}

func checkGlossary(root, ctxPath string, dirs, files []string) []string {
	if len(dirs) == 0 {
		return nil
	}
	rows := tableRows(readFile(filepath.Join(root, ctxPath)))
	if len(rows) == 0 {
		return nil
	}
	col := -1
	for i, h := range rows[0] {
		if strings.Contains(h, "避免") {
			col = i
		}
	}
	if col < 0 {
		return nil
	}
	var terms []string
	for _, r := range rows[1:] {
		if col < len(r) && !strings.Contains(strings.Join(r, ""), Placeholder) {
			for _, m := range backtick.FindAllStringSubmatch(r[col], -1) {
				terms = append(terms, m[1])
			}
		}
	}
	var probs []string
	for _, f := range files {
		in := false
		for _, d := range dirs {
			d = strings.TrimRight(d, "/")
			if f == d || strings.HasPrefix(f, d+"/") {
				in = true
			}
		}
		if !in {
			continue
		}
		for i, line := range lines(readFile(filepath.Join(root, f))) {
			for _, term := range terms {
				if regexp.MustCompile(`(^|[^\w])` + regexp.QuoteMeta(term) + `([^\w]|$)`).MatchString(line) {
					probs = append(probs, fmt.Sprintf("%s:%d: uses `%s`, which %s lists under 避免", f, i+1, term, ctxPath))
				}
			}
		}
	}
	return probs
}

func CheckDocs(root string, cfg *Config, w io.Writer) error {
	d := cfg.Docs
	var probs []string
	if agents := readFile(filepath.Join(root, d.Agents)); !exists(filepath.Join(root, d.Agents)) {
		probs = append(probs, d.Agents+" is missing")
	} else if n := len(lines(strings.TrimRight(agents, "\n"))); n > d.AgentsMaxLines {
		probs = append(probs, fmt.Sprintf("%s has %d lines > %d: it is a map, move detail into %s or into a mechanical check", d.Agents, n, d.AgentsMaxLines, d.Playbook))
	}
	top := map[string]bool{}
	for _, f := range TrackedFiles(root) {
		top[strings.SplitN(f, "/", 2)[0]] = true
	}
	legacy := map[string]bool{}
	for _, n := range LegacyNames(root, cfg) {
		legacy[filepath.ToSlash(filepath.Join(cfg.Paths.Tasks, n))] = true
	}
	maps := d.MapFiles
	if len(maps) == 0 {
		maps = []string{d.Agents, d.Fixtures, d.Invariants}
	}
	files := TrackedFiles(root)
	checked, followed := 0, 0
	var renames map[string]string
	for _, relPath := range files {
		if !strings.HasSuffix(relPath, ".md") || legacy[relPath] || GlobMatch(relPath, d.LinkExclude) {
			continue
		}
		checked++
		text := readFile(filepath.Join(root, relPath))
		noFence := fencedCode.ReplaceAllString(text, "")
		prose := inlineCode.ReplaceAllString(noFence, "")
		dir := filepath.Dir(filepath.Join(root, relPath))
		for _, m := range mdLink.FindAllStringSubmatch(prose, -1) {
			target := m[1]
			if schemeRe.MatchString(target) {
				continue
			}
			path := strings.SplitN(target, "#", 2)[0]
			switch {
			case strings.HasPrefix(path, "/"):
				probs = append(probs, fmt.Sprintf("%s: absolute link `%s` (machine-specific; it also leaks local paths)", relPath, target))
			case path != "" && !exists(filepath.Join(dir, path)):
				// A record of its day keeps its links; one whose target moved
				// is followed through git's renames instead of rewritten.
				if GlobMatch(relPath, d.Historical) {
					if renames == nil {
						renames = RenamesUpTo(root, "HEAD")
					}
					if to, ok := renames[rel(root, filepath.Join(dir, path))]; ok && exists(filepath.Join(root, to)) {
						followed++
						continue
					}
				}
				probs = append(probs, fmt.Sprintf("%s: broken link `%s`", relPath, target))
			}
		}
		if contains(maps, relPath) {
			for _, m := range backtick.FindAllStringSubmatch(noFence, -1) {
				tok := m[1]
				if !strings.Contains(tok, "/") || mapSkip.MatchString(strings.ReplaceAll(tok, "::", "")) ||
					strings.HasPrefix(tok, "http") || strings.HasPrefix(tok, "-") || strings.HasPrefix(tok, ".") {
					continue
				}
				if top[strings.SplitN(tok, "/", 2)[0]] && !mapPathExists(root, tok) && !gitIgnored(root, tok) {
					probs = append(probs, fmt.Sprintf("%s: `%s` does not exist (stale path in a map file)", relPath, tok))
				}
			}
		}
	}
	if exists(filepath.Join(root, d.Playbook)) {
		if n := len(ruleRe.FindAllString(readFile(filepath.Join(root, d.Playbook)), -1)); n > d.PlaybookMaxRules {
			probs = append(probs, fmt.Sprintf("%s has %d rules > %d: merge them, or turn the checkable ones into checks and delete the prose", d.Playbook, n, d.PlaybookMaxRules))
		}
	}
	if body, ok := sectionText(readFile(filepath.Join(root, d.Plan)), d.DecisionsHeading); ok {
		var nums []int
		for _, m := range decisionRe.FindAllStringSubmatch(body, -1) {
			n, _ := strconv.Atoi(m[1])
			nums = append(nums, n)
		}
		var bad []string
		for i := 1; i < len(nums); i++ {
			if nums[i] <= nums[i-1] {
				bad = append(bad, fmt.Sprintf("%d→%d", nums[i-1], nums[i]))
			}
		}
		if len(bad) > 0 {
			if len(bad) > 8 {
				bad = bad[:8]
			}
			probs = append(probs, fmt.Sprintf("%s: decision numbers under `%s` must be unique and increasing; out of order at %s", d.Plan, d.DecisionsHeading, strings.Join(bad, ", ")))
		}
	}
	probs = append(probs, checkRegistry(root, d.Invariants, true, files)...)
	probs = append(probs, checkRegistry(root, d.Fixtures, false, files)...)
	dates := progressRe.FindAllStringSubmatch(readFile(filepath.Join(root, d.Progress)), -1)
	var bad []string
	for i := 1; i < len(dates); i++ {
		if dates[i][1] > dates[i-1][1] {
			bad = append(bad, dates[i][1]+" after "+dates[i-1][1])
		}
	}
	if len(bad) > 0 {
		if len(bad) > 5 {
			bad = bad[:5]
		}
		probs = append(probs, fmt.Sprintf("%s: entries must be newest first; %s", d.Progress, strings.Join(bad, ", ")))
	}
	probs = append(probs, checkGlossary(root, d.Glossary, d.GlossarySourceDirs, files)...)
	note := ""
	if followed > 0 {
		note = fmt.Sprintf(", %d moved link(s) in historical documents followed", followed)
	}
	fmt.Fprintf(w, "check-docs: %d markdown file(s) checked, %d problem(s)%s\n", checked, len(probs), note)
	if len(probs) > 0 {
		return failf("%s", strings.Join(probs, "\n"))
	}
	return nil
}
