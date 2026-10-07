// Package config reads the configuration of the tool being audited: the set of
// file types it is willing to look at, and the map from path patterns to domain
// rules. Nothing here is specific to one project: which files hold the data and
// where inside them is described by a small selector language, so the same
// binary can audit any tool whose rules are "some file types, some path globs".
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Spec describes how to find the two halves of the audited configuration.
type Spec struct {
	// Allowlist is the file holding the list of accepted file extensions.
	Allowlist struct {
		File string `json:"file"`
		// Selector points at the JSON array of extensions. "" means the whole
		// file is the array.
		Selector string `json:"selector"`
	} `json:"allowlist"`

	// Rules is the file holding the default rule and the path-to-rule map.
	Rules struct {
		File string `json:"file"`
		// DefaultSelector points at the fallback rule name, for example
		// "default_rule".
		DefaultSelector string `json:"default_selector"`
		// MapSelector points at the object whose keys are glob patterns and
		// whose values are rule names, for example "path_rule_map".
		MapSelector string `json:"map_selector"`
	} `json:"rules"`

	// DocsDir is the directory holding the rule documents, relative to the
	// audited root. Empty disables the reachability check.
	DocsDir string `json:"docs_dir"`

	// IgnoreExtensions lists extensions that are expected to fall through to the
	// default rule, so they are reported as accepted rather than as findings.
	IgnoreExtensions []string `json:"ignore_extensions"`

	// PlaceholderExtensions are extensions whose entry is only there to keep a
	// formatter or a segment pattern happy, for example ".map" next to ".js".
	// They are excluded from the unrouted check.
	PlaceholderExtensions []string `json:"placeholder_extensions"`

	// IgnorePatterns lists glob patterns to leave out of the collision check,
	// for pairs that are intentional.
	IgnorePatterns []string `json:"ignore_patterns"`
}

// Config is the resolved, validated configuration of the audited tool.
type Config struct {
	Root string

	// Extensions is the allowlist, in file order and de-duplicated.
	Extensions []string

	// DefaultRule is the rule used when no pattern matches.
	DefaultRule string

	// PathRules is the path-to-rule map in declaration order, which is the order
	// a first-match-wins resolver uses.
	PathRules []PathRule

	// DocsDir is the absolute path of the rule documents directory, empty when
	// reachability is not checked.
	DocsDir string

	IgnoreExtensions      map[string]bool
	PlaceholderExtensions map[string]bool
	IgnorePatterns        []string
}

// PathRule is one entry of the path-to-rule map.
type PathRule struct {
	Pattern string
	Rule    string
	// Line is the 1-based line of the pattern inside the rules file, so that a
	// report can point at something a human can open. Zero when unknown.
	Line int
}

// LoadSpec reads the audit configuration. A missing file is an error: silently
// auditing nothing would be worse than failing.
func LoadSpec(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read audit config: %w", err)
	}
	var spec Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return Spec{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if spec.Allowlist.File == "" {
		return Spec{}, fmt.Errorf("%s: allowlist.file is required", path)
	}
	if spec.Rules.File == "" {
		return Spec{}, fmt.Errorf("%s: rules.file is required", path)
	}
	return spec, nil
}

// Load resolves the spec against a repository root.
func Load(root string, spec Spec) (*Config, error) {
	cfg := &Config{
		Root:                  root,
		IgnoreExtensions:      toSet(spec.IgnoreExtensions),
		PlaceholderExtensions: toSet(spec.PlaceholderExtensions),
		IgnorePatterns:        spec.IgnorePatterns,
	}

	allowRaw, err := readJSONFile(filepath.Join(root, spec.Allowlist.File))
	if err != nil {
		return nil, err
	}
	allowNode, err := selectNode(allowRaw, spec.Allowlist.Selector)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", spec.Allowlist.File, err)
	}
	if err := json.Unmarshal(allowNode, &cfg.Extensions); err != nil {
		return nil, fmt.Errorf("%s: allowlist is not a JSON array of strings: %w", spec.Allowlist.File, err)
	}
	cfg.Extensions = dedupe(cfg.Extensions)

	rulesRaw, err := readJSONFile(filepath.Join(root, spec.Rules.File))
	if err != nil {
		return nil, err
	}
	if spec.Rules.DefaultSelector != "" {
		node, err := selectNode(rulesRaw, spec.Rules.DefaultSelector)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.Rules.File, err)
		}
		if err := json.Unmarshal(node, &cfg.DefaultRule); err != nil {
			return nil, fmt.Errorf("%s: default selector is not a string: %w", spec.Rules.File, err)
		}
	}
	if spec.Rules.MapSelector != "" {
		node, lineOffset, err := resolveNode(rulesRaw, spec.Rules.MapSelector)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.Rules.File, err)
		}
		cfg.PathRules, err = decodeOrderedMap(node, lineOffset)
		if err != nil {
			return nil, fmt.Errorf("%s: path rule map: %w", spec.Rules.File, err)
		}
	}
	if spec.DocsDir != "" {
		cfg.DocsDir = filepath.Join(root, spec.DocsDir)
	}

	if len(cfg.Extensions) == 0 {
		return nil, fmt.Errorf("allowlist is empty")
	}
	return cfg, nil
}

func readJSONFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// selectNode walks a dotted selector such as "path_rule_map" or
// "config.file_types" and returns the raw JSON of the selected node. "[]" or an
// empty selector means the document itself.
//
// The node is returned as raw bytes, never re-encoded: re-encoding through a Go
// map would destroy the declaration order of a path-to-rule map, and that order
// is the behaviour being audited.
func selectNode(raw []byte, selector string) (json.RawMessage, error) {
	if selector == "" || selector == "[]" || selector == "." {
		return json.RawMessage(raw), nil
	}
	current := json.RawMessage(raw)
	for _, part := range strings.Split(selector, ".") {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(current, &object); err != nil {
			return nil, fmt.Errorf("selector %q: %q is not inside a JSON object", selector, part)
		}
		next, ok := object[part]
		if !ok {
			return nil, fmt.Errorf("selector %q: key %q not found", selector, part)
		}
		current = next
	}
	return current, nil
}

// resolveNode walks a dotted selector and returns the selected node's raw JSON
// along with the line offset needed to turn a position inside that subtree into a
// real line in the original document.
//
// The offset is computed by counting the newlines before the value itself: the
// decoder's InputOffset sits on the separating comma, not on the first byte of
// the value, so counting from there would overstate every line.
func resolveNode(raw []byte, selector string) (json.RawMessage, int, error) {
	if selector == "" || selector == "[]" || selector == "." {
		return json.RawMessage(raw), 0, nil
	}
	var (
		node       json.RawMessage
		lineOffset int
	)
	for _, part := range strings.Split(selector, ".") {
		dec := json.NewDecoder(bytes.NewReader(raw))
		if _, err := dec.Token(); err != nil {
			return nil, 0, fmt.Errorf("selector %q: invalid JSON: %w", selector, err)
		}
		found := false
		lineOffset = 0
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, 0, fmt.Errorf("selector %q: %w", selector, err)
			}
			var value json.RawMessage
			if err := dec.Decode(&value); err != nil {
				return nil, 0, fmt.Errorf("selector %q: %w", selector, err)
			}
			if key, ok := keyToken.(string); ok && key == part {
				node = value
				// The value's own leading newlines belong to the offset, so only
				// the whitespace that precedes the value counts here.
				if start := dec.InputOffset() - int64(len(value)); start > 0 {
					lineOffset = newlinesBefore(raw, start)
				}
				found = true
				break
			}
		}
		if !found {
			return nil, 0, fmt.Errorf("selector %q: key %q not found", selector, part)
		}
		raw = node
	}
	return node, lineOffset, nil
}

// newlinesBefore counts the complete lines before a byte offset in raw. Both
// "\n" and "\r\n" are treated as one line ending, because a configuration file
// checked out on Windows has CRLF while one read from a Git object has LF, and
// the line a human sees must be the same either way.
func newlinesBefore(raw []byte, offset int64) int {
	if offset < 0 || offset > int64(len(raw)) {
		return 0
	}
	n := 0
	for i := int64(0); i < offset; i++ {
		switch raw[i] {
		case '\n':
			n++
		case '\r':
			if i+1 >= offset || raw[i+1] != '\n' {
				n++
			}
		}
	}
	return n
}

// decodeOrderedMap preserves the declaration order of a JSON object's keys,
// which is the order a first-match-wins resolver relies on, and records the line
// each key appears on so reports can point at the file.
func decodeOrderedMap(raw []byte, lineOffset int) ([]PathRule, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("expected a JSON object, got %v", token)
	}
	var out []PathRule
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, err
		}
		// InputOffset sits right after the key token, which is on the key's own
		// line; taking it after the value instead would count the following
		// newline and shift every pattern by one.
		keyOffset := dec.InputOffset()
		key, ok := keyToken.(string)
		if !ok {
			return nil, fmt.Errorf("object key is not a string: %v", keyToken)
		}
		var rule string
		if err := dec.Decode(&rule); err != nil {
			return nil, fmt.Errorf("value for %q is not a string: %w", key, err)
		}
		out = append(out, PathRule{
			Pattern: key,
			Rule:    rule,
			Line:    lineOffset + newlinesBefore(raw, keyOffset) + 1,
		})
	}
	return out, nil
}

func toSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

func dedupe(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
