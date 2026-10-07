package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// runCapture runs the CLI with args and returns the exit code plus stdout.
func runCapture(t *testing.T, args ...string) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := 0
	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	switch args[0] {
	case "check":
		code = runCheck(args[1:])
	case "explain":
		code = runExplain(args[1:])
	default:
		t.Fatalf("unknown command %q", args[0])
	}

	_ = w.Close()
	os.Stdout = old
	<-done
	return code, buf.String()
}

// TestCheckOnCleanFixture expects the committed fixture to pass: it is the
// configuration CI runs against, so it must not report errors.
func TestCheckOnCleanFixture(t *testing.T) {
	code, out := runCapture(t, "check",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	if strings.Contains(out, "ERROR") {
		t.Fatalf("clean fixture must not report errors:\n%s", out)
	}
	if !strings.Contains(out, "shadowed-rule") {
		t.Fatalf("the fixture is meant to exercise the shadow check:\n%s", out)
	}
}

func TestCheckStrictFailsTheRun(t *testing.T) {
	code, out := runCapture(t, "check",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--strict", "--fail-on", "note")
	if code != 1 {
		t.Fatalf("exit = %d, want 1 because the fixture has an unrouted extension\n%s", code, out)
	}
}

func TestCheckJSONOutputIsValidJSON(t *testing.T) {
	code, out := runCapture(t, "check",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--format", "json")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	var parsed struct {
		Extensions int `json:"extensions"`
		Findings   []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed.Extensions != 8 {
		t.Fatalf("extensions = %d, want 8", parsed.Extensions)
	}
	if len(parsed.Findings) == 0 {
		t.Fatalf("expected the fixture findings in the JSON output")
	}
}

func TestExplainNamesTheWinningPattern(t *testing.T) {
	code, out := runCapture(t, "explain",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--path", ".github/workflows/ci.yml")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	// The catch-all yaml rule is declared first, so it wins even though the
	// workflow-specific rule is more specific. Saying so is the whole point of
	// explain.
	if !strings.Contains(out, "yaml.md") {
		t.Fatalf("expected the yaml rule to win:\n%s", out)
	}
	if !strings.Contains(out, "github_workflows.md") {
		t.Fatalf("expected the losing pattern to be listed:\n%s", out)
	}
}

func TestExplainRequiresAPath(t *testing.T) {
	code, _ := runCapture(t, "explain",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestCheckRejectsUnknownFormat(t *testing.T) {
	code, _ := runCapture(t, "check",
		"--config", "../../testdata/fixture-repo/.rulefit.json",
		"--root", "../../testdata/fixture-repo",
		"--format", "xml")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}

func TestCheckFailsOnTheRealWorldExample(t *testing.T) {
	// examples/opencodereview.json points at a repository that is not part of
	// this one, so a missing root must be a usage error rather than a panic.
	code, _ := runCapture(t, "check",
		"--config", "../../examples/opencodereview.json",
		"--root", "../../testdata/does-not-exist")
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
}
