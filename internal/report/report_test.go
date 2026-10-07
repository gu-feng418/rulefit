package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gu-feng418/rulefit/internal/check"
)

func sampleResult() *check.Result {
	return &check.Result{
		Root:             ".",
		Extensions:       8,
		Patterns:         6,
		RoutedExtensions: 8,
		Findings: []check.Finding{
			{
				Code:     check.CodeShadowedRule,
				Severity: check.SeverityWarning,
				Message:  `"**/package.json" covers "**/*.json"`,
				Pattern:  "**/package.json",
				Pattern2: "**/*.json",
				Rule:     "json.md",
				Rule2:    "package_json.md",
				Witness:  "package.json",
				Proven:   true,
				Index:    5,
				Index2:   2,
				Line:     8,
				Line2:    11,
			},
			{
				Code:     check.CodeShadowedRule,
				Severity: check.SeverityWarning,
				Message:  `two patterns overlap but no witness`,
				Proven:   false,
			},
			{
				Code:       check.CodeUnroutedExtension,
				Severity:   check.SeverityNote,
				Message:    "2 extensions fall back",
				Extensions: []string{".rb", ".rs"},
				Rule:       "default.md",
				Proven:     true,
			},
			{
				Code:     check.CodeDeadPattern,
				Severity: check.SeverityError,
				Message:  `pattern "**/pom.xml" matches nothing`,
				Pattern:  "**/pom.xml",
				Rule:     "pom_xml.md",
				Proven:   true,
				Index:    3,
			},
		},
	}
}

func TestWriteTextGroupsBySeverityAndMarksUnproven(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteText(&buf, sampleResult(), true); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{"ERROR (1)", "WARNING (2)", "[unproven]", "witness: package.json", ".rb .rs"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output is missing %q:\n%s", want, out)
		}
	}
	// Errors are printed before warnings, so a reader sees the worst first.
	if strings.Index(out, "ERROR") > strings.Index(out, "WARNING") {
		t.Errorf("errors should come before warnings:\n%s", out)
	}
}

func TestWriteTextHidesExtensionListUnlessVerbose(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteText(&buf, sampleResult(), false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), ".rb .rs") {
		t.Errorf("the extension list should be hidden without -v:\n%s", buf.String())
	}
}

func TestWriteJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, sampleResult()); err != nil {
		t.Fatal(err)
	}
	var parsed check.Result
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(parsed.Findings) != 4 {
		t.Fatalf("findings = %d, want 4", len(parsed.Findings))
	}
	if parsed.Findings[2].Extensions[1] != ".rs" {
		t.Fatalf("aggregated extensions were lost: %+v", parsed.Findings[2])
	}
}

func TestWriteSARIFExpandsAggregatedFindings(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteSARIF(&buf, sampleResult(), "rules.json", "allowlist.json"); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name string `json:"name"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID  string `json:"ruleId"`
				Level   string `json:"level"`
				Message struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if doc.Version != "2.1.0" {
		t.Fatalf("version = %q", doc.Version)
	}
	if doc.Runs[0].Tool.Driver.Name != "rulefit" {
		t.Fatalf("driver name = %q", doc.Runs[0].Tool.Driver.Name)
	}
	// 2 shadow findings + 2 aggregated unrouted extensions + 1 dead pattern.
	if len(doc.Runs[0].Results) != 5 {
		t.Fatalf("results = %d, want 5 (aggregated findings expand per item)", len(doc.Runs[0].Results))
	}
	levels := map[string]int{}
	for _, r := range doc.Runs[0].Results {
		levels[r.Level]++
	}
	if levels["error"] != 1 || levels["warning"] != 2 || levels["note"] != 2 {
		t.Fatalf("unexpected level counts: %v", levels)
	}
	// The dead-pattern finding carries a line; the sorted order puts errors first,
	// and SARIF must report the pattern's real source line, not its index.
	asserted := false
	for _, r := range doc.Runs[0].Results {
		if r.RuleID == check.CodeShadowedRule && len(r.Locations) > 0 && r.Locations[0].PhysicalLocation.Region.StartLine != 0 {
			if got := r.Locations[0].PhysicalLocation.Region.StartLine; got != 8 {
				t.Fatalf("SARIF should carry the pattern's real source line (8), got %d", got)
			}
			asserted = true
		}
	}
	if !asserted {
		t.Fatalf("no SARIF result carried a source line")
	}
}
