package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadKeepsDeclarationOrder(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "allow.json"), `[".go",".java",".go"]`)
	write(t, filepath.Join(root, "rules.json"), `{
	  "default_rule": "default.md",
	  "path_rule_map": {
	    "**/pom.xml": "pom_xml.md",
	    "**/*.java": "java.md",
	    "**/*.go": "go.md"
	  }
	}`)

	cfg, err := Load(root, Spec{
		Allowlist: struct {
			File     string `json:"file"`
			Selector string `json:"selector"`
		}{File: "allow.json"},
		Rules: struct {
			File            string `json:"file"`
			DefaultSelector string `json:"default_selector"`
			MapSelector     string `json:"map_selector"`
		}{File: "rules.json", DefaultSelector: "default_rule", MapSelector: "path_rule_map"},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRule != "default.md" {
		t.Fatalf("default rule = %q", cfg.DefaultRule)
	}
	if len(cfg.Extensions) != 2 {
		t.Fatalf("extensions = %v, want the duplicates removed", cfg.Extensions)
	}
	wantOrder := []string{"**/pom.xml", "**/*.java", "**/*.go"}
	if len(cfg.PathRules) != len(wantOrder) {
		t.Fatalf("path rules = %+v", cfg.PathRules)
	}
	for i, want := range wantOrder {
		if cfg.PathRules[i].Pattern != want {
			t.Fatalf("path rule %d = %q, want %q (declaration order matters for first-match-wins)", i, cfg.PathRules[i].Pattern, want)
		}
	}
}

func TestLoadRejectsAnEmptyAllowlist(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "allow.json"), `[]`)
	write(t, filepath.Join(root, "rules.json"), `{"default_rule":"default.md"}`)

	_, err := Load(root, Spec{
		Allowlist: allowSpec("allow.json", ""),
		Rules:     rulesSpec("rules.json", "default_rule", ""),
	})
	if err == nil {
		t.Fatalf("an empty allowlist must be an error rather than an empty audit")
	}
}

func TestLoadRejectsAMissingFile(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "rules.json"), `{"default_rule":"default.md"}`)

	_, err := Load(root, Spec{
		Allowlist: allowSpec("nope.json", ""),
		Rules:     rulesSpec("rules.json", "default_rule", ""),
	})
	if err == nil {
		t.Fatalf("expected an error for a missing allowlist file")
	}
}

func TestLoadRejectsAnUnknownSelector(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "allow.json"), `{"config":{"file_types":[".go"]}}`)
	write(t, filepath.Join(root, "rules.json"), `{"default_rule":"default.md"}`)

	_, err := Load(root, Spec{
		Allowlist: allowSpec("allow.json", "config.missing"),
		Rules:     rulesSpec("rules.json", "default_rule", ""),
	})
	if err == nil {
		t.Fatalf("expected an error for a selector that matches nothing")
	}
}

func TestLoadRejectsANonStringMapValue(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "allow.json"), `[".go"]`)
	write(t, filepath.Join(root, "rules.json"), `{"default_rule":"default.md","path_rule_map":{"**/*.go":123}}`)

	_, err := Load(root, Spec{
		Allowlist: allowSpec("allow.json", ""),
		Rules:     rulesSpec("rules.json", "default_rule", "path_rule_map"),
	})
	if err == nil {
		t.Fatalf("expected an error for a non-string rule value")
	}
}

func TestLoadSpecValidatesRequiredFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rulefit.json")

	write(t, path, `{"allowlist":{"file":"a.json"}}`)
	if _, err := LoadSpec(path); err == nil {
		t.Fatalf("expected an error when rules.file is missing")
	}

	write(t, path, `{"rules":{"file":"r.json"}}`)
	if _, err := LoadSpec(path); err == nil {
		t.Fatalf("expected an error when allowlist.file is missing")
	}

	write(t, path, `{"allowlist":{"file":"a.json"},"rules":{"file":"r.json"}}`)
	spec, err := LoadSpec(path)
	if err != nil {
		t.Fatalf("LoadSpec: %v", err)
	}
	if spec.Allowlist.File != "a.json" || spec.Rules.File != "r.json" {
		t.Fatalf("spec = %+v", spec)
	}

	if _, err := LoadSpec(filepath.Join(dir, "absent.json")); err == nil {
		t.Fatalf("expected an error for a missing audit configuration")
	}
}

func TestLoadOverridesUseTheSelectorLanguage(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "allow.json"), `{"config":{"file_types":[".go",".ts"]}}`)
	write(t, filepath.Join(root, "rules.json"), `{"linter":{"fallback":"default.md","by_path":{"**/*.go":"go.md"}}}`)

	cfg, err := Load(root, Spec{
		Allowlist: allowSpec("allow.json", "config.file_types"),
		Rules:     rulesSpec("rules.json", "linter.fallback", "linter.by_path"),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultRule != "default.md" {
		t.Fatalf("default rule = %q", cfg.DefaultRule)
	}
	if len(cfg.PathRules) != 1 || cfg.PathRules[0].Rule != "go.md" {
		t.Fatalf("path rules = %+v", cfg.PathRules)
	}
	if cfg.IgnoreExtensions[".ts"] {
		t.Fatalf("no ignore extensions were configured")
	}
}

func allowSpec(file, selector string) struct {
	File     string `json:"file"`
	Selector string `json:"selector"`
} {
	return struct {
		File     string `json:"file"`
		Selector string `json:"selector"`
	}{File: file, Selector: selector}
}

func rulesSpec(file, def, m string) struct {
	File            string `json:"file"`
	DefaultSelector string `json:"default_selector"`
	MapSelector     string `json:"map_selector"`
} {
	return struct {
		File            string `json:"file"`
		DefaultSelector string `json:"default_selector"`
		MapSelector     string `json:"map_selector"`
	}{File: file, DefaultSelector: def, MapSelector: m}
}
