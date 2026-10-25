package keel

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	kroot "github.com/visual-z/keel"
)

func templateFS() fs.FS {
	sub, err := fs.Sub(kroot.Template, "template")
	if err != nil {
		panic(err)
	}
	return sub
}

func presetNames() []string {
	entries, _ := fs.ReadDir(templateFS(), "presets")
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".toml") {
			out = append(out, strings.TrimSuffix(e.Name(), ".toml"))
		}
	}
	sort.Strings(out)
	return out
}

func presetText(stack, name string) (string, error) {
	b, err := fs.ReadFile(templateFS(), "presets/"+stack+".toml")
	if err != nil {
		return "", failf("no preset `%s`; available: %s", stack, strings.Join(presetNames(), ", "))
	}
	return expand(string(b), name), nil
}

func expand(s, name string) string {
	return strings.NewReplacer("{{PROJECT_NAME}}", name, "{{KEEL_VERSION}}", PinVersion(),
		"{{DATE}}", time.Now().Format("2006-01-02")).Replace(s)
}

func miseText() string {
	return "# Tool versions for this repository (https://mise.jdx.dev): `mise install`.\n" +
		"[tools]\n\"github:visual-z/keel\" = \"" + PinVersion() + "\"\n"
}

// writeTemplate copies the embedded template into root. Existing files are
// never overwritten; `only` restricts the copy to the given paths.
func writeTemplate(root, name string, only func(string) bool, w io.Writer) error {
	return fs.WalkDir(templateFS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(p, "presets/") || (only != nil && !only(p)) {
			return err
		}
		dst := filepath.Join(root, p)
		if exists(dst) {
			fmt.Fprintf(w, "keep   %s (exists)\n", p)
			return nil
		}
		b, err := fs.ReadFile(templateFS(), p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(p, ".githooks/") {
			mode = 0o755 // embed drops the exec bit
		}
		fmt.Fprintf(w, "write  %s\n", p)
		return os.WriteFile(dst, []byte(expand(string(b), name)), mode)
	})
}

func enableHooks(root string) {
	if _, ok := Git(root, "rev-parse", "--git-dir"); ok {
		GitIn(root, "", nil, "config", "core.hooksPath", ".githooks")
	}
}

// Init turns an empty repository into a keel project.
func Init(root, name, stack string, w io.Writer) error {
	for _, f := range []string{configFile, "AGENTS.md"} {
		if exists(filepath.Join(root, f)) {
			return failf("%s already exists; for an existing project use `keel adopt`", f)
		}
	}
	preset, err := presetText(stack, name)
	if err != nil {
		return err
	}
	if _, ok := Git(root, "rev-parse", "--git-dir"); !ok {
		if _, msg, ok := GitIn(root, "", nil, "init", "-q"); !ok {
			return failf("git init failed: %s", msg)
		}
	}
	if err := writeTemplate(root, name, nil, w); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(root, configFile), []byte(preset), 0o644); err != nil {
		return err
	}
	if !exists(filepath.Join(root, "mise.toml")) {
		_ = os.WriteFile(filepath.Join(root, "mise.toml"), []byte(miseText()), 0o644)
	}
	enableHooks(root)
	fmt.Fprintf(w, "initialized `%s` with preset `%s` (keel %s). Next: fill ⟨…⟩ in AGENTS.md, PLAN.md, CONTEXT.md\n", name, stack, PinVersion())
	return nil
}

// Adopt brings keel into an existing project. It adds only what is missing
// (keel.toml from a preset, card templates, hooks) and freezes the list of
// cards that do not comply. The list is written once; afterwards it only
// shrinks.
func Adopt(root, stack string, w io.Writer) error {
	if !exists(filepath.Join(root, configFile)) {
		if stack == "" {
			return failf("%s is missing: pass --stack <%s> to start from a preset", configFile, strings.Join(presetNames(), "|"))
		}
		name := filepath.Base(root)
		preset, err := presetText(stack, name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, configFile), []byte(preset), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(w, "write  %s (preset %s; adjust the gate to this project's definition of done)\n", configFile, stack)
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		return err
	}
	tasks := filepath.ToSlash(cfg.Paths.Tasks)
	if err := writeTemplate(root, filepath.Base(root), func(p string) bool {
		return strings.HasPrefix(p, ".githooks/") || p == "docs/tasks/TEMPLATE.md" || p == "docs/tasks/TEMPLATE-LIGHT.md"
	}, w); err != nil {
		return err
	}
	if tasks != "docs/tasks" { // templates belong where this project keeps its cards
		for _, n := range []string{"TEMPLATE.md", "TEMPLATE-LIGHT.md"} {
			src, dst := filepath.Join(root, "docs/tasks", n), filepath.Join(root, tasks, n)
			if exists(src) && !exists(dst) {
				_ = os.MkdirAll(filepath.Dir(dst), 0o755)
				_ = os.Rename(src, dst)
			}
		}
	}
	enableHooks(root)
	manifest := filepath.Join(root, cfg.Paths.LegacyManifest)
	if exists(manifest) {
		return failf("%s already exists; the legacy list is written once, at adoption", cfg.Paths.LegacyManifest)
	}
	cards := AllCardPaths(root, cfg)
	var legacy []string
	for _, p := range cards {
		c, err := LoadCard(root, p)
		if err != nil {
			return err
		}
		if len(LintCard(c, cfg.ReceiptRuleFor(c))) > 0 {
			legacy = append(legacy, filepath.Base(p))
		}
	}
	_ = os.MkdirAll(filepath.Dir(manifest), 0o755)
	header := fmt.Sprintf("# keel legacy cards, written by `keel adopt` on %s.\n"+
		"# They predate keel and keep their old format; every card not listed must comply.\n"+
		"# This list may only shrink (freeze-check). To migrate a card, fix it and delete its line.\n"+
		"# RED: lines in these cards are still claims, and audit-red still checks them.\n", time.Now().Format("2006-01-02"))
	var b strings.Builder
	b.WriteString(header)
	for _, n := range legacy {
		b.WriteString(n + "\n")
	}
	if err := os.WriteFile(manifest, []byte(b.String()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "adopt: %d legacy card(s) of %d written to %s\n", len(legacy), len(cards), cfg.Paths.LegacyManifest)
	return nil
}

var slugRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func NewCard(root string, cfg *Config, typ, slug string, light bool, w io.Writer) error {
	if !contains(CardTypes, typ) {
		return failf("type must be one of %s", strings.Join(CardTypes, ", "))
	}
	if !slugRe.MatchString(slug) {
		return failf("slug must be lowercase kebab-case")
	}
	tasks := filepath.Join(root, cfg.Paths.Tasks)
	name := "TEMPLATE.md"
	if light {
		name = "TEMPLATE-LIGHT.md"
	}
	text := readFile(filepath.Join(tasks, name))
	if text == "" {
		return failf("%s is missing; run `keel adopt` to add the card templates", filepath.Join(cfg.Paths.Tasks, name))
	}
	dst := filepath.Join(tasks, typ+"-"+slug+".md")
	if exists(dst) {
		return failf("%s already exists", rel(root, dst))
	}
	base, ok := Git(root, "rev-parse", "--short=12", "HEAD")
	if !ok {
		base = "unborn"
	}
	for _, r := range []struct{ re, v string }{{`(?m)^type:.*$`, "type: " + typ}, {`(?m)^created:.*$`, "created: " + time.Now().Format("2006-01-02")}, {`(?m)^baseline:.*$`, "baseline: " + base}} {
		re := regexp.MustCompile(r.re)
		if loc := re.FindStringIndex(text); loc != nil {
			text = text[:loc[0]] + r.v + text[loc[1]:]
		}
	}
	if err := os.WriteFile(dst, []byte(text), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(w, rel(root, dst))
	return nil
}

func Status(root string, cfg *Config, all bool, w io.Writer) error {
	type row struct{ status, typ, weight, created, name string }
	order := map[string]int{"doing": 0, "blocked": 1, "awaiting-author": 2, "todo": 3, "done": 4}
	var rows []row
	for _, p := range CardPaths(root, cfg) {
		c, err := LoadCard(root, p)
		if err != nil {
			return err
		}
		get := func(k string) string { return orStr(c.Meta[k], "?") }
		rows = append(rows, row{get("status"), get("type"), get("weight"), get("created"), filepath.Base(p)})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		oi, ok := order[rows[i].status]
		if !ok {
			oi = 9
		}
		oj, ok := order[rows[j].status]
		if !ok {
			oj = 9
		}
		if oi != oj {
			return oi < oj
		}
		return rows[i].created < rows[j].created
	})
	shown := 0
	for _, r := range rows {
		if !all && r.status == "done" {
			continue
		}
		shown++
		fmt.Fprintf(w, "%-16s %-9s %-6s %-11s %s\n", r.status, r.typ, r.weight, r.created, r.name)
	}
	note := ""
	if !all {
		note = " (done hidden; --all to show)"
	}
	if n := len(LegacyNames(root, cfg)); n > 0 {
		note += fmt.Sprintf(", %d legacy not shown", n)
	}
	fmt.Fprintf(w, "%d card(s)%s\n", shown, note)
	return nil
}

func Mutate(root string, cfg *Config, base string, w io.Writer) error {
	if len(cfg.Mutate.Command) == 0 {
		return failf("mutate.command is empty in keel.toml")
	}
	if base == "" {
		base = cfg.Guard.Base
	}
	mb, err := MergeBase(root, base)
	if err != nil {
		return err
	}
	diff, _ := Run(root, nil, 0, "", "git", "diff", "--no-color", mb)
	path := filepath.Join(root, ".keel", "cache", "mutate.diff")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(diff.Stdout), 0o644); err != nil {
		return err
	}
	cmd := make([]string, len(cfg.Mutate.Command))
	for i, a := range cfg.Mutate.Command {
		cmd[i] = strings.NewReplacer("{base}", mb, "{diff}", path).Replace(a)
	}
	fmt.Fprintf(w, "mutate: %s\n", strings.Join(cmd, " "))
	res, err := RunStreaming(root, cmd, w)
	if err != nil {
		return failf("cannot run %s: %v", cmd[0], err)
	}
	if res != 0 {
		return failf("mutation run exited %d: surviving mutants are tests that cannot tell the difference", res)
	}
	return nil
}

var conventional = regexp.MustCompile(`^(feat|fix|perf|refactor|test|docs|chore|build|ci|style|revert)(\([a-z0-9._/-]+\))?!?: .+`)

// CommitMsg enforces Conventional Commits on the first non-comment line.
func CommitMsg(path string) error {
	for _, l := range lines(readFile(path)) {
		if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
			continue
		}
		for _, p := range []string{"Merge ", "Revert ", "fixup! ", "squash! "} {
			if strings.HasPrefix(l, p) {
				return nil
			}
		}
		if !conventional.MatchString(l) {
			return failf("commit message must be `type(scope): subject` (Conventional Commits), got:\n  %s", l)
		}
		return nil
	}
	return failf("empty commit message")
}
