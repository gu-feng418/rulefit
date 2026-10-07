package check

import (
	"os"
	"path/filepath"
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

func TestRunDetectsBroadRuleDeclaredBeforeSpecificRule(t *testing.T) {
	cfg := fixture(t,
		`[".json"]`,
		`{"default_rule":"default.md","path_rule_map":{"**/*.json":"java.md","**/package.json":"special.md"}}`,
		config.Spec{})

	res := Run(cfg, false)
	shadows := findingsByCode(res, CodeShadowedRule)
	if len(shadows) != 1 {
		t.Fatalf("want exactly 1 shadow finding, got %d: %+v", len(shadows), shadows)
	}
	f := shadows[0]
	if f.Pattern != "**/*.json" || f.Pattern2 != "**/package.json" {
		t.Fatalf("unexpected pair: %+v", f)
	}
	if f.Witness != "package.json" {
		t.Fatalf("witness = %q, want %q", f.Witness, "package.json")
	}
	if !f.Proven {
		t.Fatalf("finding should be proven, got %+v", f)
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
