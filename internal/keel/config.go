package keel

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is keel.toml: the only machine configuration a project has. keel
// reads every command from here and never guesses the stack.
type Config struct {
	Keel     KeelSection
	Project  ProjectSection
	Paths    PathsSection
	Gate     GateSection
	Red      RedConfig
	Tests    TestsSection
	Guard    GuardSection
	Docs     DocsSection
	Mutate   MutateSection
	Boundary BoundarySection
}

type KeelSection struct {
	// Version pins the keel release this repository was checked with, the
	// way rust-toolchain.toml pins rustc. A different release refuses to run.
	Version string `toml:"version"`
}

type ProjectSection struct {
	Name  string `toml:"name"`
	Stack string `toml:"stack"`
}

type PathsSection struct {
	Tasks          string `toml:"tasks"`
	LegacyManifest string `toml:"legacy_manifest"`
}

type GateSection struct {
	Step []GateStep `toml:"step"`
}

type GateStep struct {
	Name     string   `toml:"name"`
	Cmd      []string `toml:"cmd"`
	Runner   string   `toml:"runner"`
	Cwd      string   `toml:"cwd"`
	TimeoutS int      `toml:"timeout_s"`
}

// RedConfig says how to run one selection of tests. [red] is the default
// runner; [red.profile.<name>] declares more for polyglot repositories.
type RedConfig struct {
	Runner    string            `toml:"runner"`
	Command   []string          `toml:"command"`
	Cwd       string            `toml:"cwd"`
	Link      []string          `toml:"link"`
	Env       map[string]string `toml:"env"`
	TimeoutS  int               `toml:"timeout_s"`
	Repeat    int               `toml:"repeat"`
	RefPrefix string            `toml:"ref_prefix"`
	// custom runner only
	SelectedRe string `toml:"selected_re"`
	FailedRe   string `toml:"failed_re"`
	InvalidRe  string `toml:"invalid_re"`
	RedExit    []int  `toml:"red_exit"`

	Profile map[string]RedConfig `toml:"profile"`
}

type TestsSection struct {
	Paths              []string `toml:"paths"`
	InlinePaths        []string `toml:"inline_paths"`
	AssertPatterns     []string `toml:"assert_patterns"`
	SkipPatterns       []string `toml:"skip_patterns"`
	SuspiciousPatterns []string `toml:"suspicious_patterns"`
}

type GuardSection struct {
	Base string `toml:"base"`
}

type DocsSection struct {
	Agents             string   `toml:"agents"`
	AgentsMaxLines     int      `toml:"agents_max_lines"`
	Playbook           string   `toml:"playbook"`
	PlaybookMaxRules   int      `toml:"playbook_max_rules"`
	Progress           string   `toml:"progress"`
	Plan               string   `toml:"plan"`
	DecisionsHeading   string   `toml:"decisions_heading"`
	Invariants         string   `toml:"invariants"`
	Fixtures           string   `toml:"fixtures"`
	Glossary           string   `toml:"glossary"`
	GlossarySourceDirs []string `toml:"glossary_source_dirs"`
	LinkExclude        []string `toml:"link_exclude"`
	MapFiles           []string `toml:"map_files"`
}

type MutateSection struct {
	Command []string `toml:"command"`
}

type BoundarySection struct {
	Rule []BoundaryRule `toml:"rule"`
}

type BoundaryRule struct {
	Name   string   `toml:"name"`
	Paths  []string `toml:"paths"`
	Except []string `toml:"except"`
	Forbid []string `toml:"forbid"`
}

const configFile = "keel.toml"

// LoadConfig reads keel.toml. Unknown keys are an error: a misspelt key is a
// check that silently does not run, and keel fails closed.
func LoadConfig(root string) (*Config, error) {
	cfg := &Config{}
	path := filepath.Join(root, configFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s not found in %s (run `keel init` or `keel adopt`)", configFile, root)
		}
		return nil, err
	}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", configFile, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown key(s) %s; a misspelt key is a check that never runs", configFile, strings.Join(keys, ", "))
	}
	// A profile inherits every field it does not set. IsDefined tells "set to
	// empty" apart from "not set", so a profile can clear an inherited list.
	for name, p := range cfg.Red.Profile {
		cfg.Red.Profile[name] = mergeProfile(cfg.Red, p, func(field string) bool {
			return md.IsDefined("red", "profile", name, field)
		})
	}
	cfg.applyDefaults()
	return cfg, nil
}

func mergeProfile(base, p RedConfig, set func(string) bool) RedConfig {
	out := base
	out.Profile = nil
	if set("runner") {
		out.Runner = p.Runner
	}
	if set("command") {
		out.Command = p.Command
	}
	if set("cwd") {
		out.Cwd = p.Cwd
	}
	if set("link") {
		out.Link = p.Link
	}
	if set("env") {
		out.Env = p.Env
	}
	if set("timeout_s") {
		out.TimeoutS = p.TimeoutS
	}
	if set("repeat") {
		out.Repeat = p.Repeat
	}
	if set("selected_re") {
		out.SelectedRe = p.SelectedRe
	}
	if set("failed_re") {
		out.FailedRe = p.FailedRe
	}
	if set("invalid_re") {
		out.InvalidRe = p.InvalidRe
	}
	if set("red_exit") {
		out.RedExit = p.RedExit
	}
	if set("ref_prefix") {
		out.RefPrefix = p.RefPrefix
	}
	return out
}

func orStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (c *Config) applyDefaults() {
	c.Paths.Tasks = orStr(c.Paths.Tasks, "docs/tasks")
	c.Paths.LegacyManifest = orStr(c.Paths.LegacyManifest, "docs/tasks/legacy.txt")
	c.Guard.Base = orStr(c.Guard.Base, "main")
	d := &c.Docs
	d.Agents = orStr(d.Agents, "AGENTS.md")
	d.Playbook = orStr(d.Playbook, "docs/PLAYBOOK.md")
	d.Progress = orStr(d.Progress, "docs/PROGRESS.md")
	d.Plan = orStr(d.Plan, "PLAN.md")
	d.DecisionsHeading = orStr(d.DecisionsHeading, "决策记录")
	d.Invariants = orStr(d.Invariants, "docs/INVARIANTS.md")
	d.Fixtures = orStr(d.Fixtures, "docs/FIXTURES.md")
	d.Glossary = orStr(d.Glossary, "CONTEXT.md")
	if d.AgentsMaxLines == 0 {
		d.AgentsMaxLines = 150
	}
	if d.PlaybookMaxRules == 0 {
		d.PlaybookMaxRules = 120
	}
	fill := func(r *RedConfig) {
		r.Cwd = orStr(r.Cwd, ".")
		r.RefPrefix = orStr(r.RefPrefix, "refs/keel/red/")
		if r.TimeoutS == 0 {
			r.TimeoutS = 1800
		}
		if r.Repeat == 0 {
			r.Repeat = 1
		}
	}
	fill(&c.Red)
	for name, p := range c.Red.Profile {
		fill(&p)
		c.Red.Profile[name] = p
	}
}

// RedFor returns the runner settings a claim selects with RED(<profile>).
func (c *Config) RedFor(profile string) (RedConfig, error) {
	if profile == "" {
		base := c.Red
		base.Profile = nil
		return base, nil
	}
	p, ok := c.Red.Profile[profile]
	if !ok {
		names := make([]string, 0, len(c.Red.Profile))
		for n := range c.Red.Profile {
			names = append(names, n)
		}
		sort.Strings(names)
		declared := "none"
		if len(names) > 0 {
			declared = strings.Join(names, ", ")
		}
		return RedConfig{}, fmt.Errorf("unknown RED profile `%s`; declared: %s", profile, declared)
	}
	return p, nil
}

// StepNames lists the configured gate steps, in order.
func (c *Config) StepNames() []string {
	out := make([]string, 0, len(c.Gate.Step))
	for _, s := range c.Gate.Step {
		out = append(out, s.Name)
	}
	return out
}
