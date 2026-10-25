package keel

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type gateOpts struct {
	record    string
	keepGoing bool
	only      []string
	tier      string
}

// Gate runs every configured step, reads each exit code directly, parses
// test counts where a runner is declared (a test step that runs nothing is
// not green) and prints a one-line receipt. Full output goes to .keel/logs.
//
// With --record the receipt is also written into the card, backed by a
// record of the tested tree (see record.go), and the card's whole work is
// guarded from its anchor. Only a passing gate is recorded.
func Gate(root string, cfg *Config, o gateOpts, w io.Writer) error {
	if len(cfg.Gate.Step) == 0 {
		return failf("keel.toml has no [[gate.step]]; a gate with nothing in it passes vacuously")
	}
	if len(o.only) > 0 && o.record != "" {
		return failf("--record needs the full gate: a run restricted with --only is not a completion receipt")
	}
	if o.tier != "" && !contains(cfg.Tiers(), o.tier) {
		return usagef("unknown tier `%s`; declared: %s", o.tier, orStr(strings.Join(cfg.Tiers(), ", "), "none"))
	}
	var card *Card
	anchor := ""
	if o.record != "" {
		c, err := LoadCard(root, o.record)
		if err != nil {
			return err
		}
		card = c
		sha, _, ok, err := CardAnchor(root, card)
		if err != nil {
			return err
		}
		if ok {
			anchor = sha
		}
		if missing := missingTiers(cfg, card, o.tier); len(missing) > 0 {
			return failf("%s asks for the `%s` tier; run `keel gate --tier %s --record %s`", card.Rel, missing[0], missing[0], o.record)
		}
	}
	tested, err := BoundTree(root, cfg, false)
	if err != nil {
		return err
	}
	logs := filepath.Join(root, ".keel", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	var results, notes []string
	ok := true
	for _, step := range cfg.Gate.Step {
		if step.Tier != "" && step.Tier != o.tier {
			continue
		}
		if len(o.only) > 0 && !contains(o.only, step.Name) {
			continue
		}
		if len(step.Cmd) == 0 {
			return failf("gate step `%s` has no cmd", step.Name)
		}
		fmt.Fprintf(w, "gate  %-12s %s\n", step.Name, strings.Join(step.Cmd, " "))
		start := time.Now()
		logPath := filepath.Join(logs, stamp+"-"+step.Name+".log")
		code, extra, failed, err := runStep(root, cfg, root, step, logPath)
		if err != nil {
			return err
		}
		if code != 0 && len(failed) > 0 {
			fmt.Fprintf(w, "      failed: %s\n", strings.Join(failed, "; "))
		}
		if code != 0 && step.Retries > 0 && len(failed) > 0 && len(failed) <= step.MaxFlaky {
			flaky, err := retryFailed(root, cfg, step, failed, w)
			if err != nil {
				return err
			}
			if len(flaky) == len(failed) {
				code = 0
				extra = strings.TrimSuffix(extra, "]")
				extra = regexp.MustCompile(`/\d+f`).ReplaceAllString(extra, "/0f") + fmt.Sprintf("/%dflaky]", len(flaky))
				for _, f := range flaky {
					notes = append(notes, fmt.Sprintf("flaky: %s: %s (failed in the full run, passed alone %d time(s))", step.Name, f, step.Retries))
				}
			}
		}
		result := fmt.Sprintf("%s=%d%s", step.Name, code, extra)
		if code != 0 && step.External && anchor != "" {
			wt, err := NewWorktree(root, anchor, cfg.Red)
			if err != nil {
				return err
			}
			base, _, _, err := runStep(root, cfg, wt.Path, step, logPath+".base")
			wt.Close()
			if err != nil {
				return err
			}
			if base != 0 {
				fmt.Fprintf(w, "      the card's anchor %s fails this step too (exit %d): not caused by this card\n", anchor[:12], base)
				result += "(base)"
				notes = append(notes, fmt.Sprintf("external: %s exits %d here and %d at the anchor %s", step.Name, code, base, anchor[:12]))
				code = 0
			}
		}
		results = append(results, result)
		fmt.Fprintf(w, "      exit %d %s %.1fs  log: %s\n", code, extra, time.Since(start).Seconds(), rel(root, logPath))
		if code != 0 {
			ok = false
			if !o.keepGoing {
				break
			}
		}
	}
	if card != nil && (ok || o.keepGoing) {
		// The card's whole work, not only what is uncommitted: from its anchor.
		var buf bytes.Buffer
		guardCard := ""
		if anchor != "" {
			guardCard = o.record
		}
		gerr := AssertGuard(root, cfg, "", guardCard, &buf)
		fmt.Fprintf(w, "gate  %-12s %s\n", "guard", strings.TrimSpace(buf.String()))
		code := 0
		if gerr != nil {
			code = 1
			ok = false
			fmt.Fprintf(w, "      %s\n", gerr)
		}
		results = append(results, fmt.Sprintf("guard=%d", code))
	}
	verdict := "fail"
	if ok {
		verdict = "ok"
	}
	receipt := fmt.Sprintf("GATE: %s %s tree:%s %s (%s)", HeadLabel(root), verdict, tested[:12], strings.Join(results, " "), time.Now().Format("2006-01-02"))
	fmt.Fprintln(w, receipt)
	for _, n := range notes {
		fmt.Fprintln(w, "      "+n)
	}
	if !ok {
		if o.record != "" {
			fmt.Fprintln(w, "not recorded: only a passing gate is a completion receipt")
		}
		return failf("gate failed")
	}
	if o.record != "" {
		after, err := BoundTree(root, cfg, false)
		if err != nil {
			return err
		}
		if after != tested {
			return failf("the tree changed while the gate ran (%s → %s); nothing recorded, run it again", tested[:12], after[:12])
		}
		if err := WriteRecord(root, tested, receipt, notes); err != nil {
			return err
		}
		if err := recordReceipt(root, o.record, receipt); err != nil {
			return err
		}
		fmt.Fprintf(w, "recorded in %s\n", o.record)
	}
	return nil
}

// missingTiers: the tiers a card asks for (`gate: release`) that this run
// does not include.
func missingTiers(cfg *Config, card *Card, tier string) []string {
	var out []string
	for _, t := range cardTiers(card) {
		if t != tier {
			out = append(out, t)
		}
	}
	return out
}

func cardTiers(card *Card) []string {
	var out []string
	for _, t := range strings.FieldsFunc(card.Meta["gate"], func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, t)
	}
	return out
}

// runStep runs one gate step in tree and returns its effective exit code,
// the count annotation and the failed test names.
func runStep(root string, cfg *Config, tree string, step GateStep, logPath string) (int, string, []string, error) {
	junitDir, _ := os.MkdirTemp("", "keel-junit-")
	defer os.RemoveAll(junitDir)
	junit := filepath.Join(junitDir, "report.xml")
	cmd := make([]string, len(step.Cmd))
	for i, a := range step.Cmd {
		cmd[i] = strings.ReplaceAll(a, "{junit}", junit)
	}
	isolated := filepath.Clean(tree) != filepath.Clean(root)
	res, err := Run(filepath.Join(tree, orStr(step.Cwd, ".")), TestEnv(root, cfg.Red, isolated, junit),
		time.Duration(step.TimeoutS)*time.Second, "", cmd[0], cmd[1:]...)
	code := res.Code
	switch {
	case err != nil:
		code = 127
		res.Out = err.Error()
	case res.TimedOut:
		code = 124
	}
	_ = os.WriteFile(logPath, []byte(res.Out), 0o644)
	if step.Runner == "" || err != nil {
		return code, "", nil, nil
	}
	s, serr := summarize(cfg.Red, step.Runner, res.Out, junit)
	if serr != nil {
		return 0, "", nil, serr
	}
	skip := ""
	if s.Skipped > 0 {
		skip = fmt.Sprintf("/%dskip", s.Skipped)
	}
	extra := fmt.Sprintf("[%dp/%df%s]", s.Selected-s.Failed, s.Failed, skip)
	switch {
	case s.Invalid != "":
		if code == 0 {
			code = 1
		}
		return code, "[" + s.Invalid + "]", nil, nil
	case s.Selected == 0:
		if code == 0 {
			code = 1
		}
		return code, "[0 tests: a test step that runs nothing is not green]", nil, nil
	}
	// Names are only trusted when they account for every failure: a failure
	// keel cannot name is not one it may retry away.
	if len(s.FailedNames) != s.Failed || !redExitOK(RedConfig{Runner: step.Runner}, code) {
		return code, extra, nil, nil
	}
	return code, extra, s.FailedNames, nil
}

// retryFailed re-runs each failed test alone, step.Retries times, with the
// step's [red] profile. A test is a flake only if every lone run passes; one
// that fails alone too is a real failure. Returns the flakes.
func retryFailed(root string, cfg *Config, step GateStep, names []string, w io.Writer) ([]string, error) {
	red, err := cfg.RedFor(step.Profile)
	if err != nil {
		return nil, err
	}
	var flaky []string
	for _, name := range names {
		filter := retryFilter(red, name)
		green := 0
		for n := 0; n < step.Retries; n++ {
			out, err := RunFilter(root, red, root, filter)
			if err != nil {
				return nil, err
			}
			if RequireGreen(out, "retry") != nil {
				break
			}
			green++
		}
		if green == step.Retries {
			flaky = append(flaky, name)
			fmt.Fprintf(w, "      retry %s: passed alone %d/%d: a flake\n", name, green, step.Retries)
		} else {
			fmt.Fprintf(w, "      retry %s: failed alone (%d/%d passed): a real failure\n", name, green, step.Retries)
		}
	}
	return flaky, nil
}

// retryFilter turns a failed test's name into a filter that selects it in
// the [red] command: bun's -t and go's -run are regular expressions.
func retryFilter(red RedConfig, name string) string {
	args := false
	for _, a := range red.Command {
		if a == "{args}" {
			args = true
		}
	}
	switch red.Runner {
	case "bun":
		parts := strings.Split(name, " > ")
		return regexp.QuoteMeta(parts[len(parts)-1])
	case "go-json":
		pkg, test, _ := strings.Cut(name, " ")
		var levels []string
		for _, l := range strings.Split(test, "/") {
			levels = append(levels, "^"+regexp.QuoteMeta(l)+"$")
		}
		run := strings.Join(levels, "/")
		if args {
			return "-run '" + run + "' " + pkg
		}
		return run
	}
	return name
}

// recordReceipt appends the receipt to the card's `## 证据` section.
func recordReceipt(root, card, line string) error {
	p := card
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return failf("%s does not exist", card)
	}
	ls := lines(strings.TrimRight(string(b), "\n"))
	idx := -1
	for i, l := range ls {
		if strings.HasPrefix(l, "## 证据") {
			idx = i
			break
		}
	}
	if idx < 0 {
		ls = append(ls, "", "## 证据", "")
		idx = len(ls) - 2
	}
	end := len(ls)
	for j := idx + 1; j < len(ls); j++ {
		if strings.HasPrefix(ls[j], "## ") {
			end = j
			break
		}
	}
	body := append([]string{}, ls[idx+1:end]...)
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}
	out := append(append([]string{}, ls[:idx+1]...), "")
	out = append(out, body...)
	out = append(out, line)
	if rest := ls[end:]; len(rest) > 0 {
		out = append(out, "")
		out = append(out, rest...)
	}
	return os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o644)
}
