// Command rulefit audits the "which files do we review, and which rule applies"
// configuration of a code-review or static-analysis tool.
//
// It answers the questions that are invisible in a large path-to-rule map:
// which allowlisted file types reach no language rule, which rule documents are
// unreachable, which patterns are dead, and which pattern pairs compete because
// the resolver stops at the first match.
//
// Exit status: 0 when nothing at or above the failure level was found, 2 for
// usage errors, and 1 when findings fail the run.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gu-feng418/rulefit/internal/check"
	"github.com/gu-feng418/rulefit/internal/config"
	"github.com/gu-feng418/rulefit/internal/glob"
	"github.com/gu-feng418/rulefit/internal/report"
)

const usage = `rulefit audits path-to-rule configuration.

usage:
  rulefit check   --config rulefit.json [--root DIR] [--strict] [-v]
  rulefit explain --config rulefit.json --path FILE

commands:
  check     report extensions without a rule, unreachable rules, dead patterns
            and pattern pairs that compete for the same files
  explain   show which pattern wins for one path, in declaration order

exit status:
  0  nothing failed the run
  1  findings failed the run (errors, plus notes when --strict is set)
  2  usage error
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "explain":
		os.Exit(runExplain(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("rulefit check", flag.ContinueOnError)
	configPath := fs.String("config", "rulefit.json", "path to the audit configuration")
	root := fs.String("root", ".", "repository root to audit")
	format := fs.String("format", "text", "output format: text, json or sarif")
	strict := fs.Bool("strict", false, "treat unrouted extensions as errors")
	verbose := fs.Bool("v", false, "list every unrouted extension instead of summarising")
	failOn := fs.String("fail-on", "error", "fail the run on: error, warning or note")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	spec, err := config.LoadSpec(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rulefit:", err)
		return 2
	}
	cfg, err := config.Load(*root, spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rulefit:", err)
		return 2
	}

	res := check.Run(cfg, *strict)

	switch *format {
	case "text":
		if err := report.WriteText(os.Stdout, res, *verbose); err != nil {
			fmt.Fprintln(os.Stderr, "rulefit:", err)
			return 2
		}
	case "json":
		if err := report.WriteJSON(os.Stdout, res); err != nil {
			fmt.Fprintln(os.Stderr, "rulefit:", err)
			return 2
		}
	case "sarif":
		if err := report.WriteSARIF(os.Stdout, res, spec.Rules.File, spec.Allowlist.File); err != nil {
			fmt.Fprintln(os.Stderr, "rulefit:", err)
			return 2
		}
	default:
		fmt.Fprintf(os.Stderr, "rulefit: unknown format %q (want text, json or sarif)\n", *format)
		return 2
	}

	threshold, ok := severityOf(*failOn)
	if !ok {
		fmt.Fprintf(os.Stderr, "rulefit: unknown -fail-on value %q (want error, warning or note)\n", *failOn)
		return 2
	}
	if fails(res, threshold) {
		return 1
	}
	return 0
}

// runExplain prints the resolution of one path, which is the fastest way to
// answer "why did this file get that rule".
func runExplain(args []string) int {
	fs := flag.NewFlagSet("rulefit explain", flag.ContinueOnError)
	configPath := fs.String("config", "rulefit.json", "path to the audit configuration")
	root := fs.String("root", ".", "repository root to audit")
	path := fs.String("path", "", "path to resolve, relative to the repository root")
	asJSON := fs.Bool("json", false, "print the resolution as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *path == "" {
		fmt.Fprintln(os.Stderr, "rulefit explain: -path is required")
		return 2
	}

	spec, err := config.LoadSpec(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rulefit:", err)
		return 2
	}
	cfg, err := config.Load(*root, spec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rulefit:", err)
		return 2
	}

	resolution := explain(cfg, filepath.ToSlash(*path))
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resolution); err != nil {
			fmt.Fprintln(os.Stderr, "rulefit:", err)
			return 2
		}
		return 0
	}

	fmt.Printf("path:      %s\n", resolution.Path)
	fmt.Printf("reviewed:  %v\n", resolution.Reviewed)
	fmt.Printf("rule:      %s\n", resolution.Rule)
	if resolution.Pattern != "" {
		fmt.Printf("matched:   %q (line %d of the path rule map)\n", resolution.Pattern, resolution.Index+1)
	} else {
		fmt.Println("matched:   no pattern; the default rule applies")
	}
	if len(resolution.AlsoMatched) > 0 {
		fmt.Println("also matches (never consulted, first match wins):")
		for _, m := range resolution.AlsoMatched {
			fmt.Printf("  %-40s line %-4d %s\n", m.Pattern, m.Index+1, m.Rule)
		}
	}
	return 0
}

// Resolution describes how the audited tool would treat one path.
type Resolution struct {
	Path         string   `json:"path"`
	Extension    string   `json:"extension"`
	Reviewed     bool     `json:"reviewed"`
	Rule         string   `json:"rule"`
	Pattern      string   `json:"pattern,omitempty"`
	Index        int      `json:"index,omitempty"`
	AlsoMatched  []Match  `json:"also_matched,omitempty"`
}

// Match is one additional pattern that would have matched.
type Match struct {
	Pattern string `json:"pattern"`
	Rule    string `json:"rule"`
	Index   int    `json:"index"`
}

func explain(cfg *config.Config, path string) Resolution {
	res := Resolution{Path: path}
	if i := lastDot(path); i >= 0 {
		res.Extension = path[i:]
	}
	for _, ext := range cfg.Extensions {
		if ext == res.Extension {
			res.Reviewed = true
			break
		}
	}
	for i, pr := range cfg.PathRules {
		if !match(pr.Pattern, path) {
			continue
		}
		if res.Pattern == "" {
			res.Pattern = pr.Pattern
			res.Rule = pr.Rule
			res.Index = i
			continue
		}
		res.AlsoMatched = append(res.AlsoMatched, Match{Pattern: pr.Pattern, Rule: pr.Rule, Index: i})
	}
	if res.Pattern == "" {
		res.Rule = cfg.DefaultRule
	}
	return res
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return -1
		}
		if s[i] == '.' {
			return i
		}
	}
	return -1
}

func match(pattern, path string) bool {
	return glob.Parse(pattern).Match(path)
}

func severityOf(name string) (check.Severity, bool) {
	switch name {
	case "error":
		return check.SeverityError, true
	case "warning":
		return check.SeverityWarning, true
	case "note":
		return check.SeverityNote, true
	default:
		return "", false
	}
}

// fails reports whether any finding is at or above the threshold.
func fails(res *check.Result, threshold check.Severity) bool {
	rank := map[check.Severity]int{
		check.SeverityError:   0,
		check.SeverityWarning: 1,
		check.SeverityNote:    2,
	}
	for _, f := range res.Findings {
		if rank[f.Severity] <= rank[threshold] {
			return true
		}
	}
	return false
}

