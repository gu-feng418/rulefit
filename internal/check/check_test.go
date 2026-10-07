package check

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gu-feng418/rulefit/internal/config"
)

// fixture writes a minimal tool configuration and returns the resolved Config.
func fixture(t *testing.T, allowlist string, systemRules string, spec config.Spec) *config.Config {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "allowlist.json"), allowlist)
	writeFile(t, filepath.Join(root, "system_rules.json"), systemRules)
	writeFile(t, filepath.Join(root, "docs", "default.md"), "# default")
	for _, rule := range []string{"go.md", "java.md", "yaml.md", "special.md"} {
		writeFile(t, filepath.Join(root, "docs", rule), "# "+rule)
	}
	if spec.Allowlist.File == "" {
		spec.Allowlist.File = "allowlist.json"
	}
	if spec.Rules.File == "" {
		spec.Rules.File = "system_rules.json"
	}
	if spec.Rules.DefaultSelector == "" {
		spec.Rules.DefaultSelector = "default_rule"
	}
	if spec.Rules.MapSelector == "" {
		spec.Rules.MapSelector = "path_rule_map"
	}
	if spec.DocsDir == "" {
		spec.DocsDir = "docs"
	}
	cfg, err := config.Load(root, spec)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func findingsByCode(res *Result, code string) []Finding {
	var out []Finding
	for _, f := range res.Findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func TestRunReportsShadowingWithAProvenWitness(t *testing.T) {
	cfg := fixture(t,
		`[".go", ".java"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.java":"java.md","**/*.go":"go.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	shadows := findingsByCode(res, CodeShadowedRule)
	// Nothing here competes: .go and .java are disjoint.
	if len(shadows) != 0 {
		t.Fatalf("unexpected shadow findings: %+v", shadows)
	}
	if res.RoutedExtensions != 2 {
		t.Fatalf("routed = %d, want 2", res.RoutedExtensions)
	}
}

func TestRunStrictTurnsUnroutedExtensionsIntoErrors(t *testing.T) {
	cfg := fixture(t,
		`[".go", ".rb", ".rs"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md"}}`,
		config.Spec{})

	lenient := Run(cfg, false)
	unrouted := findingsByCode(lenient, CodeUnroutedExtension)
	if len(unrouted) != 1 || unrouted[0].Severity != SeverityNote {
		t.Fatalf("lenient run should note the unrouted extensions, got %+v", unrouted)
	}
	if len(unrouted[0].Extensions) != 2 {
		t.Fatalf("unrouted extensions = %v, want .go and .rb", unrouted[0].Extensions)
	}

	strict := Run(cfg, true)
	unrouted = findingsByCode(strict, CodeUnroutedExtension)
	if len(unrouted) != 1 || unrouted[0].Severity != SeverityError {
		t.Fatalf("strict run should error on the unrouted extensions, got %+v", unrouted)
	}
}

func TestRunAggregatesASingleUnroutedExtension(t *testing.T) {
	cfg := fixture(t,
		`[".go", ".rb", ".rs"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md","**/*.rs":"java.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	unrouted := findingsByCode(res, CodeUnroutedExtension)
	if len(unrouted) != 1 {
		t.Fatalf("want 1 finding, got %+v", unrouted)
	}
	if unrouted[0].Extension != ".rb" {
		t.Fatalf("extension = %q, want .rb", unrouted[0].Extension)
	}
	if len(unrouted[0].Extensions) != 0 {
		t.Fatalf("a single extension should not be aggregated: %v", unrouted[0].Extensions)
	}
}

func TestRunIgnoresExtensionsOnTheIgnoreList(t *testing.T) {
	cfg := fixture(t,
		`[".go", ".rb"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md"}}`,
		config.Spec{IgnoreExtensions: []string{".rb"}})

	res := Run(cfg, true)
	if got := findingsByCode(res, CodeUnroutedExtension); len(got) != 0 {
		t.Fatalf("ignored extension should not be reported, got %+v", got)
	}
}

func TestRunFindsDeadPatterns(t *testing.T) {
	// .rs is reviewable, so its pattern is alive; .kt is not allowlisted, so the
	// pattern pointing at java.md can never be selected.
	cfg := fixture(t,
		`[".go", ".rs"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md","**/*.rs":"java.md","**/*.kt":"special.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	dead := findingsByCode(res, CodeDeadPattern)
	if len(dead) != 1 {
		t.Fatalf("want 1 dead-pattern finding, got %d: %+v", len(dead), dead)
	}
	if dead[0].Severity != SeverityError {
		t.Fatalf("a dead pattern is an error, got %q", dead[0].Severity)
	}
}

func TestRunFindsUnreachableRuleDocuments(t *testing.T) {
	// Both patterns are alive, but nothing maps to java.md any more: the rule
	// document is declared and unreachable.
	cfg := fixture(t,
		`[".go", ".rs"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md","**/*.rs":"go.md","**/*.kt":"java.md"}}`,
		config.Spec{})
	// Point the pattern that declares java.md at a file type that is allowlisted,
	// which leaves the document declared but reached by nothing: java.md is only
	// selected if its pattern matches first, and go.md's pattern is declared
	// earlier, so java.md never wins.
	for i := range cfg.PathRules {
		if cfg.PathRules[i].Rule == "java.md" {
			cfg.PathRules[i].Pattern = "**/*.go"
		}
	}

	res := Run(cfg, false)
	unreachable := findingsByCode(res, CodeUnreachableRule)
	if len(unreachable) != 1 || unreachable[0].Rule != "java.md" {
		t.Fatalf("want java.md unreachable, got %+v", unreachable)
	}
}

func TestRunReportsMissingRuleDocuments(t *testing.T) {
	cfg := fixture(t,
		`[".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"nowhere.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	missing := findingsByCode(res, CodeMissingRuleDocument)
	if len(missing) != 1 || missing[0].Rule != "nowhere.md" {
		t.Fatalf("want nowhere.md reported missing, got %+v", missing)
	}
}

func TestRunReportsUnbalancedPattern(t *testing.T) {
	cfg := fixture(t,
		`[".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.{go":"go.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	got := findingsByCode(res, CodeUnbalancedPattern)
	if len(got) != 1 {
		t.Fatalf("want 1 unbalanced-pattern finding, got %+v", got)
	}
	if got[0].Severity != SeverityError {
		t.Fatalf("severity = %q, want error", got[0].Severity)
	}
}

func TestRunHonoursIgnorePatterns(t *testing.T) {
	spec := config.Spec{IgnorePatterns: []string{"**/package.json"}}
	cfg := fixture(t,
		`[".json"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.json":"java.md","**/package.json":"special.md"}}`,
		spec)

	res := Run(cfg, false)
	if got := findingsByCode(res, CodeShadowedRule); len(got) != 0 {
		t.Fatalf("ignored pair should not be reported, got %+v", got)
	}
}

func TestRunWithSelectorBasedSpec(t *testing.T) {
	cfg := fixture(t,
		`{"config":{"file_types":[".go",".java"]}}`,
		`{"rules":{"default":"default.md","by_path":{"**/*.go":"go.md"}}}`,
		config.Spec{
			Allowlist: struct {
				File     string `json:"file"`
				Selector string `json:"selector"`
			}{File: "allowlist.json", Selector: "config.file_types"},
			Rules: struct {
				File            string `json:"file"`
				DefaultSelector string `json:"default_selector"`
				MapSelector     string `json:"map_selector"`
			}{File: "system_rules.json", DefaultSelector: "rules.default", MapSelector: "rules.by_path"},
		})

	if cfg.DefaultRule != "default.md" {
		t.Fatalf("default rule = %q", cfg.DefaultRule)
	}
	if len(cfg.PathRules) != 1 || cfg.PathRules[0].Pattern != "**/*.go" {
		t.Fatalf("path rules = %+v", cfg.PathRules)
	}
	res := Run(cfg, false)
	if len(findingsByCode(res, CodeDeadPattern)) != 0 {
		t.Fatalf("no dead patterns expected: %+v", res.Findings)
	}
}

// TestRunKeepsWholeFileNameRulesAlive pins the probe semantics: a rule naming a
// whole file must stay alive when the allowlist permits that file's extension.
// Getting this wrong reported every "**/pom.xml"-style rule as dead, which is
// exactly the false positive that probes taken only from the allowlist produce.
func TestRunKeepsWholeFileNameRulesAlive(t *testing.T) {
	cfg := fixture(t,
		`[".go", ".xml", ".json", ".toml"]`,
		`{"default_rule":"default.md","path_rule_map":{
		  "**/pom.xml":"go.md",
		  "**/package.json":"java.md",
		  "**/Cargo.toml":"yaml.md",
		  "**/*{Mapper,Dao}*.xml":"special.md",
		  "**/*.go":"go.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	if got := findingsByCode(res, CodeDeadPattern); len(got) != 0 {
		t.Fatalf("whole-file rules with allowlisted extensions must stay alive, got %+v", got)
	}
	if got := findingsByCode(res, CodeUnreachableRule); len(got) != 0 {
		t.Fatalf("no rule document should be unreachable here, got %+v", got)
	}
	if got := findingsByCode(res, CodeShadowedRule); len(got) != 0 {
		t.Fatalf("specific-before-general must not be flagged as a defect, got %+v", got)
	}
}

// TestRunFlagsTheGeneralBeforeSpecificOrdering is the counterpart, and the shape
// of the first real defect rulefit found: a general pattern declared before a
// specific one silently wins for the files the specific rule was written for.
func TestRunFlagsTheGeneralBeforeSpecificOrdering(t *testing.T) {
	cfg := fixture(t,
		`[".json"]`,
		`{"default_rule":"default.md","path_rule_map":{
		  "**/*.json":"json.md",
		  "**/package.json":"special.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	shadows := findingsByCode(res, CodeShadowedRule)
	if len(shadows) != 1 {
		t.Fatalf("want exactly 1 shadow finding, got %d: %+v", len(shadows), shadows)
	}
	if shadows[0].Rule2 != "special.md" {
		t.Fatalf("the finding should name the rule that loses, got %+v", shadows[0])
	}
	if !strings.Contains(shadows[0].Message, "special.md can never be selected") {
		t.Fatalf("message should say which rule loses: %s", shadows[0].Message)
	}
}

// TestRunReportsExtensionNotInAllowlist is the other half of that pin: a rule
// pinning an extension the allowlist does not contain is genuinely dead, and the
// probe must not manufacture a path for it out of the pattern's own text.
func TestRunReportsExtensionNotInAllowlist(t *testing.T) {
	cfg := fixture(t,
		`[".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md","**/*.kt":"java.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	dead := findingsByCode(res, CodeDeadPattern)
	if len(dead) != 1 || dead[0].Pattern != "**/*.kt" {
		t.Fatalf("want **/*.kt reported dead, got %+v", dead)
	}
}

// TestRunCaseSensitivityIsHonoured documents the trap that the first real audit
// found: an allowlist holding only ".r" cannot cover a rule written "**/*.R" —
// unless the audited tool folds case, which is what CaseInsensitive declares.
func TestRunCaseSensitivityIsHonoured(t *testing.T) {
	lowerOnly := fixture(t,
		`[".r", ".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.R":"go.md","**/*.go":"go.md"}}`,
		config.Spec{})
	if got := findingsByCode(Run(lowerOnly, false), CodeDeadPattern); len(got) != 1 {
		t.Fatalf("**/*.R must be dead when only .r is allowlisted, got %+v", got)
	}

	bothCases := fixture(t,
		`[".r", ".R", ".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.R":"go.md","**/*.go":"go.md"}}`,
		config.Spec{})
	if got := findingsByCode(Run(bothCases, false), CodeDeadPattern); len(got) != 0 {
		t.Fatalf("**/*.R is alive once .R is allowlisted, got %+v", got)
	}
}

// TestRunFoldsCaseWhenTheToolDoes pins the fact that made the first audit's
// headline finding a false positive: alibaba/open-code-review lower-cases both
// the pattern and the path (and lower-cases the extension when consulting its
// allowlist), so "**/*.R" against an allowlist entry of ".r" is consistent for
// that tool, even though it is inconsistent as written text.
func TestRunFoldsCaseWhenTheToolDoes(t *testing.T) {
	spec := config.Spec{CaseInsensitive: true}

	cfg := fixture(t,
		`[".r", ".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.R":"go.md","**/*.go":"go.md"}}`,
		spec)
	if got := findingsByCode(Run(cfg, false), CodeDeadPattern); len(got) != 0 {
		t.Fatalf("a case-folding tool makes **/*.R reachable through .r, got %+v", got)
	}
	if got := findingsByCode(Run(cfg, false), CodeUnreachableRule); len(got) != 0 {
		t.Fatalf("go.md is reachable, got %+v", got)
	}

	// The same configuration must still report a genuinely dead pattern.
	dead := fixture(t,
		`[".go"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.go":"go.md","**/*.KT":"java.md"}}`,
		spec)
	if got := findingsByCode(Run(dead, false), CodeDeadPattern); len(got) != 1 {
		t.Fatalf("case folding must not revive **/*.KT, got %+v", got)
	}
}

// TestRunReportsSourceLines checks that findings name the line a human can open,
// not the pattern's index inside the map.
func TestRunReportsSourceLines(t *testing.T) {
	cfg := fixture(t,
		`[".go", ".json"]`,
		`{"default_rule":"default.md","path_rule_map":{
		  "**/*.json":"java.md",
		  "**/package.json":"special.md",
		  "**/*.go":"go.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	shadows := findingsByCode(res, CodeShadowedRule)
	if len(shadows) != 1 {
		t.Fatalf("want 1 shadow finding, got %+v", shadows)
	}
	if shadows[0].Line == 0 || shadows[0].Line2 == 0 {
		t.Fatalf("findings must carry source lines, got %d and %d", shadows[0].Line, shadows[0].Line2)
	}
	if shadows[0].Line2 != shadows[0].Line+1 {
		t.Fatalf("the two patterns are on adjacent lines, got %d and %d", shadows[0].Line, shadows[0].Line2)
	}
	if !strings.Contains(shadows[0].Message, fmt.Sprintf("(line %d)", shadows[0].Line)) {
		t.Fatalf("message should name the real line: %s", shadows[0].Message)
	}
}

func TestRunDoesNotReportDisjointExtensionsAsShadowing(t *testing.T) {
	cfg := fixture(t,
		`[".properties", ".xml"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.properties":"java.md","**/*.xml":"go.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	if got := findingsByCode(res, CodeShadowedRule); len(got) != 0 {
		t.Fatalf("disjoint extensions must not collide, got %+v", got)
	}
}
