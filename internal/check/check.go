// Package check turns a resolved configuration into findings. Every finding is
// derived from the configuration itself 鈥?no repository scan, no guessing 鈥?so
// the same input always produces the same output, which is what makes the tool
// usable as a CI gate.
package check

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gu-feng418/rulefit/internal/config"
	"github.com/gu-feng418/rulefit/internal/glob"
)

// Severity orders findings. Only Error fails a strict run.
type Severity string

const (
	// SeverityError marks a configuration defect that is certainly wrong.
	SeverityError Severity = "error"
	// SeverityWarning marks a likely defect or an intentional-but-fragile setup.
	SeverityWarning Severity = "warning"
	// SeverityNote marks something worth knowing that is not a defect.
	SeverityNote Severity = "note"
)

// Finding codes, stable so that CI configuration and ignore lists can name them.
const (
	CodeShadowedRule        = "shadowed-rule"
	CodeUnroutedExtension   = "unrouted-extension"
	CodeUnreachableRule     = "unreachable-rule"
	CodeDeadPattern         = "dead-pattern"
	CodeUnbalancedPattern   = "unbalanced-pattern"
	CodeMissingRuleDocument = "missing-rule-document"
)

// Finding is one reported problem.
type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	// Pattern and Pattern2 name the patterns involved, when there are two.
	Pattern  string `json:"pattern,omitempty"`
	Pattern2 string `json:"pattern2,omitempty"`
	// Rule and Rule2 name the rules involved.
	Rule  string `json:"rule,omitempty"`
	Rule2 string `json:"rule2,omitempty"`
	// Extension is set for per-extension findings.
	Extension string `json:"extension,omitempty"`
	// Extensions carries the affected extensions when a finding aggregates many
	// of them, which keeps a report with dozens of unrouted types readable.
	Extensions []string `json:"extensions,omitempty"`
	// Patterns carries the affected patterns when a finding aggregates them.
	Patterns []string `json:"patterns,omitempty"`
	// Witness is a concrete path both patterns match, empty when unproven.
	Witness string `json:"witness,omitempty"`
	// Proven reports whether the finding rests on a constructed, verified path.
	Proven bool `json:"proven"`
	// Index and Index2 are the declaration positions in the path-to-rule map.
	Index  int `json:"index,omitempty"`
	Index2 int `json:"index2,omitempty"`
}

// Result is the complete outcome of one audit.
type Result struct {
	Root             string    `json:"root"`
	Extensions       int       `json:"extensions"`
	Patterns         int       `json:"patterns"`
	RoutedExtensions int       `json:"routed_extensions"`
	Findings         []Finding `json:"findings"`
}

// Count returns how many findings carry the given severity.
func (r Result) Count(severity Severity) int {
	n := 0
	for _, f := range r.Findings {
		if f.Severity == severity {
			n++
		}
	}
	return n
}

// Sorted returns findings ordered by severity, then by code, for stable output.
func (r Result) Sorted() []Finding {
	out := make([]Finding, len(r.Findings))
	copy(out, r.Findings)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) < severityRank(out[j].Severity)
		}
		if out[i].Code != out[j].Code {
			return out[i].Code < out[j].Code
		}
		return out[i].Message < out[j].Message
	})
	return out
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// Run audits a resolved configuration.
//
// strict decides whether unrouted extensions are errors. That is the one policy
// question the tool cannot answer for the user: falling back to a default rule is
// legitimate for a tool with a generic rule, and a defect for one without.
func Run(cfg *config.Config, strict bool) *Result {
	res := &Result{
		Root:       cfg.Root,
		Extensions: len(cfg.Extensions),
		Patterns:   len(cfg.PathRules),
	}

	patterns := make([]glob.Pattern, len(cfg.PathRules))
	for i, pr := range cfg.PathRules {
		patterns[i] = glob.Parse(pr.Pattern)
		if patterns[i].Warning != "" {
			res.Findings = append(res.Findings, Finding{
				Code:     CodeUnbalancedPattern,
				Severity: SeverityError,
				Message:  fmt.Sprintf("pattern %q is outside the supported glob dialect: %s", pr.Pattern, patterns[i].Warning),
				Pattern:  pr.Pattern,
				Rule:     pr.Rule,
				Index:    i,
				Proven:   true,
			})
		}
	}

	checkMissingDocuments(cfg, res)
	checkReachability(cfg, patterns, res)
	checkDeadPatterns(cfg, patterns, res)
	checkShadows(cfg, patterns, res)
	checkUnrouted(cfg, patterns, strict, res)
	checkRouted(cfg, patterns, res)

	return res
}

// checkShadows finds pattern pairs that compete for the same paths. Declaration
// order decides the winner, which is exactly the kind of thing that is invisible
// in a large map and silently changes behaviour when a line is reordered.
func checkShadows(cfg *config.Config, patterns []glob.Pattern, res *Result) {
	accept := extensionAcceptor(cfg)
	for i := 0; i < len(cfg.PathRules); i++ {
		for j := i + 1; j < len(cfg.PathRules); j++ {
			left, right := cfg.PathRules[i], cfg.PathRules[j]
			if left.Pattern == right.Pattern || left.Rule == right.Rule {
				// The same pattern or the same rule cannot change behaviour,
				// since both branches land on the same document.
				continue
			}
			if matchesAnyPattern(cfg.IgnorePatterns, left.Pattern) || matchesAnyPattern(cfg.IgnorePatterns, right.Pattern) {
				continue
			}
			witness := glob.FindOverlap(patterns[i], patterns[j], accept)
			if witness == nil {
				continue
			}
			res.Findings = append(res.Findings, shadowFinding(left, right, i, j, witness))
		}
	}
}

func shadowFinding(left, right config.PathRule, i, j int, w *glob.Witness) Finding {
	f := Finding{
		Code:     CodeShadowedRule,
		Severity: SeverityWarning,
		Pattern:  left.Pattern,
		Pattern2: right.Pattern,
		Rule:     left.Rule,
		Rule2:    right.Rule,
		Index:    i,
		Index2:   j,
		Witness:  w.Path,
		Proven:   w.Confidence == glob.ConfidenceExact,
	}
	if !f.Proven {
		f.Message = fmt.Sprintf("%q (line %d) and %q (line %d) overlap, but no concrete path could be built to show which rule wins", left.Pattern, i+1, right.Pattern, j+1)
		return f
	}
	switch w.Kind {
	case glob.KindAShadowsB:
		f.Message = fmt.Sprintf("%q (line %d) already claims every path of %q (line %d); %s can never be selected", left.Pattern, i+1, right.Pattern, j+1, right.Rule)
	case glob.KindBShadowsA:
		f.Message = fmt.Sprintf("%q (line %d) covers everything %q (line %d) claims and more; %s wins on its own paths", right.Pattern, j+1, left.Pattern, i+1, left.Rule)
	default:
		f.Message = fmt.Sprintf("%q (line %d) and %q (line %d) share paths; the first declared wins there, so %s is unreachable for them", left.Pattern, i+1, right.Pattern, j+1, right.Rule)
	}
	return f
}

// checkUnrouted reports allowlisted extensions that reach no language rule, so
// the tool falls back to its default rule for them.
func checkUnrouted(cfg *config.Config, patterns []glob.Pattern, strict bool, res *Result) {
	severity := SeverityNote
	if strict {
		severity = SeverityError
	}
	var unrouted []string
	for _, ext := range cfg.Extensions {
		if cfg.IgnoreExtensions[ext] || cfg.PlaceholderExtensions[ext] {
			continue
		}
		if routed(cfg, patterns, ext) {
			continue
		}
		unrouted = append(unrouted, ext)
	}
	if len(unrouted) == 0 {
		return
	}
	if len(unrouted) == 1 {
		res.Findings = append(res.Findings, Finding{
			Code:      CodeUnroutedExtension,
			Severity:  severity,
			Message:   fmt.Sprintf("%s is reviewed but no pattern routes it to a language rule; it falls back to %s", unrouted[0], cfg.DefaultRule),
			Extension: unrouted[0],
			Rule:      cfg.DefaultRule,
			Proven:    true,
		})
		return
	}
	res.Findings = append(res.Findings, Finding{
		Code:       CodeUnroutedExtension,
		Severity:   severity,
		Message:    fmt.Sprintf("%d of %d allowlisted extensions route to no language rule and fall back to %s", len(unrouted), len(cfg.Extensions), cfg.DefaultRule),
		Extensions: unrouted,
		Rule:       cfg.DefaultRule,
		Proven:     true,
	})
}

// checkRouted counts allowlisted extensions that do reach a language rule, which
// is the headline number of the report.
func checkRouted(cfg *config.Config, patterns []glob.Pattern, res *Result) {
	for _, ext := range cfg.Extensions {
		if routed(cfg, patterns, ext) {
			res.RoutedExtensions++
		}
	}
}

// routed reports whether an allowlisted extension reaches a language rule. Two
// probes are used because rules key on both shapes: "probe.go" for an extension
// glob and ".map" for a bare name.
func routed(cfg *config.Config, patterns []glob.Pattern, ext string) bool {
	for _, probe := range []string{"probe" + ext, ext} {
		for i := range cfg.PathRules {
			if !patterns[i].Match(probe) {
				continue
			}
			if cfg.PathRules[i].Rule != cfg.DefaultRule {
				return true
			}
		}
	}
	return false
}

// checkReachability reports rule documents that no allowlisted file can reach,
// which usually means a rule was written and then disconnected.
func checkReachability(cfg *config.Config, patterns []glob.Pattern, res *Result) {
	if cfg.DocsDir == "" {
		return
	}
	declared := map[string]bool{}
	var order []string
	add := func(rule string) {
		if rule == "" || declared[rule] {
			return
		}
		declared[rule] = true
		order = append(order, rule)
	}
	add(cfg.DefaultRule)
	for _, pr := range cfg.PathRules {
		add(pr.Rule)
	}
	sort.Strings(order)

	reachable := map[string]bool{}
	if cfg.DefaultRule != "" {
		reachable[cfg.DefaultRule] = true
	}
	// Only the first matching pattern counts: a later pattern that also matches
	// a file type is not the one the resolver selects, so it does not make its
	// rule reachable.
	for _, candidate := range probePaths(cfg) {
		for i := range patterns {
			if patterns[i].Match(candidate) {
				reachable[cfg.PathRules[i].Rule] = true
				break
			}
		}
	}

	for _, rule := range order {
		if reachable[rule] {
			continue
		}
		res.Findings = append(res.Findings, Finding{
			Code:     CodeUnreachableRule,
			Severity: SeverityError,
			Message:  fmt.Sprintf("rule document %q is declared but no allowlisted file type can reach it", rule),
			Rule:     rule,
			Proven:   true,
		})
	}
}

// checkMissingDocuments reports declared rule documents that do not exist on
// disk, which a resolver would hit at runtime.
func checkMissingDocuments(cfg *config.Config, res *Result) {
	if cfg.DocsDir == "" {
		return
	}
	missing := func(rule string) bool {
		_, err := os.Stat(filepath.Join(cfg.DocsDir, rule))
		return err != nil
	}
	seen := map[string]bool{}
	for _, pr := range cfg.PathRules {
		if pr.Rule == "" || seen[pr.Rule] {
			continue
		}
		seen[pr.Rule] = true
		if !missing(pr.Rule) {
			continue
		}
		res.Findings = append(res.Findings, Finding{
			Code:     CodeMissingRuleDocument,
			Severity: SeverityError,
			Message:  fmt.Sprintf("pattern %q maps to %q, which does not exist in the rule documents directory", pr.Pattern, pr.Rule),
			Pattern:  pr.Pattern,
			Rule:     pr.Rule,
			Proven:   true,
		})
	}
	if cfg.DefaultRule != "" && !seen[cfg.DefaultRule] && missing(cfg.DefaultRule) {
		res.Findings = append(res.Findings, Finding{
			Code:     CodeMissingRuleDocument,
			Severity: SeverityError,
			Message:  fmt.Sprintf("default rule %q does not exist in the rule documents directory", cfg.DefaultRule),
			Rule:     cfg.DefaultRule,
			Proven:   true,
		})
	}
}

// checkDeadPatterns reports patterns that can never match a file the tool would
// review, because no allowlisted file type can produce a matching path. It is
// the most common way for a rule to go missing: the rule document exists, but
// nothing can reach it.
func checkDeadPatterns(cfg *config.Config, patterns []glob.Pattern, res *Result) {
	candidates := probePaths(cfg)
	var dead []Finding
	for i, pr := range cfg.PathRules {
		if patterns[i].Warning != "" {
			continue // already reported as an invalid pattern
		}
		alive := false
		for _, candidate := range candidates {
			if patterns[i].Match(candidate) {
				alive = true
				break
			}
		}
		if alive {
			continue
		}
		dead = append(dead, Finding{
			Code:     CodeDeadPattern,
			Severity: SeverityError,
			Message:  fmt.Sprintf("pattern %q matches no allowlisted file type, so rule %q can never be selected", pr.Pattern, pr.Rule),
			Pattern:  pr.Pattern,
			Rule:     pr.Rule,
			Index:    i,
			Proven:   true,
		})
	}
	if len(dead) == 1 {
		res.Findings = append(res.Findings, dead[0])
		return
	}
	if len(dead) > 1 {
		names := make([]string, 0, len(dead))
		for _, f := range dead {
			names = append(names, fmt.Sprintf("%s (%s)", f.Pattern, f.Rule))
		}
		sort.Strings(names)
		res.Findings = append(res.Findings, Finding{
			Code:     CodeDeadPattern,
			Severity: SeverityError,
			Message:  fmt.Sprintf("%d patterns match no allowlisted file type and can never be selected: %s", len(dead), strings.Join(names, ", ")),
			Patterns: names,
			Proven:   true,
		})
	}
}

// probePaths builds the file shapes that path rules can key on, then places each
// of them under the directory prefixes the rules name.
//
// Every probe is derived from the allowlist alone. That is deliberate: probes
// taken from the patterns themselves would let a pattern certify itself, which
// is how a whole-file rule such as "**/*.kt" would look alive even though no
// allowlisted file type can ever be a ".kt" file.
func probePaths(cfg *config.Config) []string {
	var names []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	// With a stem, without one, and case-folded: rules are written in all three
	// shapes in real allowlists ("**/*.go", "**/Makefile", "**/*.R").
	for _, ext := range cfg.Extensions {
		bare := strings.TrimPrefix(ext, ".")
		add("probe" + ext)
		add(bare)
		add(strings.ToUpper(bare))
		add(strings.ToLower(bare))
		// Directory-anchored rules such as "**/.github/**" need a directory
		// probe too, which the prefix loop below supplies.
	}

	var out []string
	for _, dir := range probeDirs(cfg) {
		for _, name := range names {
			out = append(out, dir+name)
		}
	}
	return out
}

// probeDirs returns the directory prefixes to place probe names in, taken from
// the leading literal segments of every pattern.
func probeDirs(cfg *config.Config) []string {
	dirs := []string{"", "dir/", "probe/"}
	seen := map[string]bool{"": true, "dir/": true, "probe/": true}
	for _, pr := range cfg.PathRules {
		pattern := glob.Parse(pr.Pattern)
		for _, alt := range pattern.Alternates {
			parts := make([]string, 0, len(alt.Segments))
			for _, seg := range alt.Segments {
				if seg.DoubleStar {
					break
				}
				frag, ok := glob.SelfSegmentGlob(seg)
				if !ok || frag == "" || strings.ContainsAny(frag, "*?[") {
					break
				}
				parts = append(parts, frag)
			}
			if len(parts) == 0 {
				continue
			}
			for _, dir := range []string{strings.Join(parts, "/") + "/", strings.Join(parts, "/") + "/dir/"} {
				if !seen[dir] {
					seen[dir] = true
					dirs = append(dirs, dir)
				}
			}
		}
	}
	return dirs
}

// extensionAcceptor turns the allowlist into the predicate the collision check
// uses, so that a shared path must be a file the tool would actually review.
func extensionAcceptor(cfg *config.Config) func(string) bool {
	allowed := make(map[string]bool, len(cfg.Extensions))
	for _, ext := range cfg.Extensions {
		allowed[ext] = true
	}
	return func(path string) bool {
		if i := strings.LastIndexByte(path, '.'); i >= 0 {
			return allowed[path[i:]]
		}
		return false
	}
}

func matchesAnyPattern(patterns []string, subject string) bool {
	for _, p := range patterns {
		if glob.Parse(p).Match(subject) {
			return true
		}
	}
	return false
}

