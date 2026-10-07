package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// runCapture runs the CLI with args and returns the exit code plus stdout and
// stderr. Errors go to stderr, and a test that only reads stdout cannot tell a
// message it did not expect from no message at all.
func runCapture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr

	var bufOut, bufErr bytes.Buffer
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(&bufOut, rOut)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(&bufErr, rErr)
		done <- struct{}{}
	}()

	code := 0
	switch args[0] {
	case "check":
		code = runCheck(args[1:])
	case "explain":
		code = runExplain(args[1:])
	default:
		t.Fatalf("unknown command %q", args[0])
	}

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	<-done
	<-done
	return code, bufOut.String(), bufErr.String()
}

// TestCheckOnCleanFixture expects the committed clean fixture to pass. It is the
// configuration CI runs against, so it must stay silent: a healthy,
// specific-before-general map produces nothing at all.
func TestCheckOnCleanFixture(t *testing.T) {
	code, out, _ := runCapture(t, "check",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out, "no findings") {
		t.Fatalf("a well-ordered map should be silent:\n%s", out)
	}
}

// TestCheckFlagsTheDefectsFixture is the other half: the fixture carrying one
// ordering defect, one unreachable rule and one unrouted file type must report all
// three, each at its own severity.
func TestCheckFlagsTheDefectsFixture(t *testing.T) {
	code, out, _ := runCapture(t, "check",
		"--config", "../../testdata/fixture-defects/.rulefit.json",
		"--root", "../../testdata/fixture-defects")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 because the fixture has an error finding\n%s", code, out)
	}
	for _, want := range []string{"ERROR (1)", "WARNING (1)", "NOTE (1)", "shadowed-rule", "unreachable-rule", "unrouted-extension", "witness: package.json"} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
}

func TestCheckStrictPromotesUnroutedExtensions(t *testing.T) {
	code, out, _ := runCapture(t, "check",
		"--config", "../../testdata/fixture-defects/.rulefit.json",
		"--root", "../../testdata/fixture-defects",
		"--strict", "--fail-on", "note")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(out, "ERROR (2)") {
		t.Fatalf("--strict should promote the unrouted extension to an error:\n%s", out)
	}
}

func TestCheckJSONOutputIsValidJSON(t *testing.T) {
	code, out, _ := runCapture(t, "check",
		"--config", "../../testdata/fixture-defects/.rulefit.json",
		"--root", "../../testdata/fixture-defects",
		"--format", "json")
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	var parsed struct {
		Extensions int `json:"extensions"`
		Findings   []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
			Line     int    `json:"line"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed.Extensions != 6 {
		t.Fatalf("extensions = %d, want 6", parsed.Extensions)
	}
	if len(parsed.Findings) != 3 {
		t.Fatalf("findings = %d, want 3: %+v", len(parsed.Findings), parsed.Findings)
	}
	found := false
	for _, f := range parsed.Findings {
		if f.Code == "shadowed-rule" && f.Line == 6 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the shadow finding should carry line 6: %+v", parsed.Findings)
	}
}

func TestExplainNamesTheWinningPattern(t *testing.T) {
	// The defects fixture is the one where the general rule is declared first, so
	// explain must name the general rule as the winner and list the specific rule
	// as the one that never gets consulted.
	code, out, _ := runCapture(t, "explain",
		"--config", "../../testdata/fixture-defects/.rulefit.json",
		"--root", "../../testdata/fixture-defects",
		"--path", "package.json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, `matched:   "**/*.{json,json5}" (line 6)`) {
		t.Fatalf("explain should name the winning pattern and its real line:\n%s", out)
	}
	if !strings.Contains(out, "package_json.md") {
		t.Fatalf("explain should list the rule that loses:\n%s", out)
	}
}

func TestExplainFlagsAnUnreviewableExtension(t *testing.T) {
	// .json5 is allowlisted but no pattern routes it, so the default rule applies
	// and the path is still reviewed. The same command on a file type outside the
	// allowlist prints the note that says the rule is never consulted.
	code, out, _ := runCapture(t, "explain",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--path", "config/settings.json5")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "reviewed:  true") {
		t.Fatalf(".json5 is allowlisted and should read as reviewed:\n%s", out)
	}
	if !strings.Contains(out, `matched:   "**/*.{json,json5}" (line 5)`) {
		t.Fatalf("json5 should be routed by the brace pattern:\n%s", out)
	}

	code, out, _ = runCapture(t, "explain",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--path", "analysis/plot.r")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "reviewed:  false") || !strings.Contains(out, "never consulted") {
		t.Fatalf("an unlisted file type should say so:\n%s", out)
	}
}

func TestExplainFoldsCaseWhenTheToolDoes(t *testing.T) {
	// testdata/fixture-defects declares case_insensitive, so a .R path must be
	// reported as reviewed and routed, the same way the audited tool treats it.
	code, out, _ := runCapture(t, "explain",
		"--config", "../../testdata/fixture-defects/.rulefit.json",
		"--root", "../../testdata/fixture-defects",
		"--path", "ANALYSIS/PLOT.R")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "reviewed:  true") {
		t.Fatalf("a case-folding tool should review .R through the .r entry:\n%s", out)
	}
}

func TestExplainRequiresAPath(t *testing.T) {
	code, _, _ := runCapture(t, "explain",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestCheckRejectsUnknownFormat(t *testing.T) {
	code, _, _ := runCapture(t, "check",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--format", "xml")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestCheckFailsOnTheRealWorldExample(t *testing.T) {
	// examples/opencodereview.json points at a repository that is not part of this
	// one, so a missing root must be reported as the missing file it is - not as a
	// parse error, which is what a stray byte-order mark produces. Asserting the
	// message keeps that distinction honest.
	code, _, errOut := runCapture(t, "check",
		"--config", "../../examples/opencodereview.json",
		"--root", "../../testdata/does-not-exist")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(errOut, "supported_file_types.json") {
		t.Fatalf("the missing allowlist file should be named, got: %q", errOut)
	}
}
