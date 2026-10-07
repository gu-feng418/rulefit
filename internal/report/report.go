// Package report renders audit results. Text output is for humans, JSON for
// other tools, and SARIF for code-scanning UIs such as GitHub's.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/gu-feng418/rulefit/internal/check"
)

// WriteText prints a compact, greppable report.
func WriteText(w io.Writer, res *check.Result, verbose bool) error {
	findings := res.Sorted()

	bySeverity := map[check.Severity][]check.Finding{}
	for _, f := range findings {
		bySeverity[f.Severity] = append(bySeverity[f.Severity], f)
	}

	fmt.Fprintf(w, "rulefit: %s\n", res.Root)
	fmt.Fprintf(w, "  %d file extensions, %d path patterns, %d extensions with a language rule\n\n",
		res.Extensions, res.Patterns, res.RoutedExtensions)

	if len(findings) == 0 {
		fmt.Fprintln(w, "no findings")
		return nil
	}

	for _, severity := range []check.Severity{check.SeverityError, check.SeverityWarning, check.SeverityNote} {
		group := bySeverity[severity]
		if len(group) == 0 {
			continue
		}
		fmt.Fprintf(w, "%s (%d)\n", strings.ToUpper(string(severity)), len(group))
		for _, f := range group {
			unproven := ""
			if !f.Proven {
				unproven = " [unproven]"
			}
			fmt.Fprintf(w, "  [%s]%s %s\n", f.Code, unproven, f.Message)
			if f.Witness != "" {
				fmt.Fprintf(w, "      witness: %s\n", f.Witness)
			}
			if verbose && len(f.Extensions) > 0 {
				fmt.Fprintf(w, "      %s\n", strings.Join(f.Extensions, " "))
			}
		}
		fmt.Fprintln(w)
	}
	return nil
}

// WriteJSON prints the full result, including unproven findings and the
// statistics behind them.
func WriteJSON(w io.Writer, res *check.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}

// sarif is the minimal SARIF 2.1.0 shape GitHub code scanning accepts.
type sarif struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules,omitempty"`
}

type sarifRule struct {
	ID               string       `json:"id"`
	ShortDescription sarifMessage `json:"shortDescription"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

// WriteSARIF prints findings in SARIF 2.1.0` so that CI can annotate the files
// that define the patterns.
func WriteSARIF(w io.Writer, res *check.Result, rulesFile, allowlistFile string) error {
	doc := sarif{
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name: "rulefit",
				Rules: []sarifRule{
					{ID: check.CodeShadowedRule, ShortDescription: sarifMessage{Text: "Two path rules compete for the same files; declaration order decides which one wins"}},
					{ID: check.CodeUnroutedExtension, ShortDescription: sarifMessage{Text: "Allowlisted file type reaches no language rule"}},
					{ID: check.CodeUnreachableRule, ShortDescription: sarifMessage{Text: "Declared rule document is unreachable"}},
					{ID: check.CodeDeadPattern, ShortDescription: sarifMessage{Text: "Pattern can never match an allowlisted file type"}},
					{ID: check.CodeUnbalancedPattern, ShortDescription: sarifMessage{Text: "Pattern is outside the supported glob dialect"}},
					{ID: check.CodeMissingRuleDocument, ShortDescription: sarifMessage{Text: "Rule document does not exist"}},
				},
			}},
		}},
	}

	for _, f := range res.Sorted() {
		level := "note"
		switch f.Severity {
		case check.SeverityError:
			level = "error"
		case check.SeverityWarning:
			level = "warning"
		}
		message := f.Message
		if !f.Proven {
			message += " (unproven: no concrete path could be constructed)"
		}
		uri := rulesFile
		if f.Code == check.CodeUnroutedExtension || f.Code == check.CodeDeadPattern {
			uri = allowlistFile
		}

		// An aggregated finding becomes one SARIF result per affected item, so
		// that a code-scanning UI can annotate each of them.
		items := 1
		if n := len(f.Extensions); n > 0 {
			items = n
		} else if n := len(f.Patterns); n > 0 {
			items = n
		}
		for k := 0; k < items; k++ {
			text := message
			switch {
			case len(f.Extensions) > 0:
				text = fmt.Sprintf("%s is reviewed but no pattern routes it to a language rule; it falls back to %s", f.Extensions[k], f.Rule)
			case len(f.Patterns) > 0:
				text = f.Patterns[k]
			}
			result := sarifResult{
				RuleID:  f.Code,
				Level:   level,
				Message: sarifMessage{Text: text},
			}
			if uri != "" {
				loc := sarifLocation{PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{URI: uri},
				}}
				if f.Line > 0 {
					loc.PhysicalLocation.Region = &sarifRegion{StartLine: f.Line}
				}
				result.Locations = []sarifLocation{loc}
			}
			doc.Runs[0].Results = append(doc.Runs[0].Results, result)
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
