package keel

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type gateOpts struct {
	record    string
	keepGoing bool
	only      []string
}

// Gate runs every configured step, reads each exit code directly, parses
// test counts where a runner is declared (a test step that runs nothing is
// not green) and prints a one-line receipt. Full output goes to .keel/logs.
func Gate(root string, cfg *Config, o gateOpts, w io.Writer) error {
	if len(cfg.Gate.Step) == 0 {
		return failf("keel.toml has no [[gate.step]]; a gate with nothing in it passes vacuously")
	}
	if len(o.only) > 0 && o.record != "" {
		return failf("--record needs the full gate: a run restricted with --only is not a completion receipt")
	}
	logs := filepath.Join(root, ".keel", "logs")
	if err := os.MkdirAll(logs, 0o755); err != nil {
		return err
	}
	stamp := time.Now().Format("20060102-150405")
	var results []string
	ok := true
	for _, step := range cfg.Gate.Step {
		if len(o.only) > 0 && !contains(o.only, step.Name) {
			continue
		}
		if len(step.Cmd) == 0 {
			return failf("gate step `%s` has no cmd", step.Name)
		}
		logPath := filepath.Join(logs, stamp+"-"+step.Name+".log")
		junitDir, _ := os.MkdirTemp("", "keel-junit-")
		junit := filepath.Join(junitDir, "report.xml")
		cmd := make([]string, len(step.Cmd))
		for i, a := range step.Cmd {
			cmd[i] = strings.ReplaceAll(a, "{junit}", junit)
		}
		fmt.Fprintf(w, "gate  %-12s %s\n", step.Name, strings.Join(step.Cmd, " "))
		start := time.Now()
		res, err := Run(filepath.Join(root, orStr(step.Cwd, ".")), TestEnv(root, cfg.Red, false, junit),
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
		extra := ""
		if step.Runner != "" && err == nil {
			s, serr := summarize(cfg.Red, step.Runner, res.Out, junit)
			if serr != nil {
				os.RemoveAll(junitDir)
				return serr
			}
			skip := ""
			if s.Skipped > 0 {
				skip = fmt.Sprintf("/%dskip", s.Skipped)
			}
			extra = fmt.Sprintf("[%dp/%df%s]", s.Selected-s.Failed, s.Failed, skip)
			switch {
			case s.Invalid != "":
				if code == 0 {
					code = 1
				}
				extra = "[" + s.Invalid + "]"
			case s.Selected == 0:
				if code == 0 {
					code = 1
				}
				extra = "[0 tests: a test step that runs nothing is not green]"
			}
		}
		os.RemoveAll(junitDir)
		results = append(results, fmt.Sprintf("%s=%d%s", step.Name, code, extra))
		fmt.Fprintf(w, "      exit %d %s %.1fs  log: %s\n", code, extra, time.Since(start).Seconds(), rel(root, logPath))
		if code != 0 {
			ok = false
			if !o.keepGoing {
				break
			}
		}
	}
	verdict := "fail"
	if ok {
		verdict = "ok"
	}
	receipt := fmt.Sprintf("GATE: %s %s %s (%s)", HeadLabel(root), verdict, strings.Join(results, " "), time.Now().Format("2006-01-02"))
	fmt.Fprintln(w, receipt)
	if o.record != "" {
		if err := recordReceipt(root, o.record, receipt); err != nil {
			return err
		}
		fmt.Fprintf(w, "recorded in %s\n", o.record)
	}
	if !ok {
		return failf("gate failed")
	}
	return nil
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
