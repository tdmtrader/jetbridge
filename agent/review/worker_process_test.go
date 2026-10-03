package review

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/session/codextest"
)

func TestMain(m *testing.M) { codextest.Main(m) }

// model names a model the pinned release's bundled catalog knows.
const model = "gpt-5.5"

func assessment(t *testing.T, complete bool, findings ...Finding) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"summary": "Inspected the change.", "complete": complete,
		"reviewed_files": []string{"parser.go", "deleted.txt"}, "limitations": []string{}, "findings": append([]Finding{}, findings...)})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func finding(location Location) Finding {
	return Finding{ID: "provisional", Severity: "high", Dimension: "correctness", Title: "Incorrect first-byte handling",
		Explanation: "The change no longer returns the first byte.", Recommendation: "Preserve first-byte behavior.", Location: location}
}

type review struct {
	bundle          *Bundle
	runtime, output string
	model           *codextest.Model
}

// runReview runs the review worker, minus its tmpfs gate, with the pinned
// Codex answering to a model scripted by turns. It checks what holds after
// every review: the session is gone and no credential reached the report.
func runReview(t *testing.T, turns ...codextest.Turn) (*review, *Report, error) {
	t.Helper()
	r := &review{bundle: captureTest(t), runtime: t.TempDir(), output: filepath.Join(t.TempDir(), "report"), model: codextest.NewModel(t, turns...)}
	report, err := runInRuntime(context.Background(), WorkerOptions{Input: r.bundle.Dir, Output: r.output, RuntimeDir: r.runtime,
		Codex: codextest.Launcher(t, r.model), ReaderCommand: codextest.Worker(t), Model: model,
		Auth: io.NopCloser(bytes.NewReader(codextest.Auth())), Timeout: time.Minute})
	if entries, _ := os.ReadDir(r.runtime); len(entries) != 0 {
		t.Fatalf("the session survived: %v", entries)
	}
	filepath.WalkDir(r.output, func(path string, d os.DirEntry, err error) error {
		if b, _ := os.ReadFile(path); bytes.Contains(b, []byte(codextest.Secret)) {
			t.Errorf("the credential reached %s", path)
		}
		return nil
	})
	return r, report, err
}

func (r *review) unpublished(t *testing.T) {
	t.Helper()
	if _, err := os.Lstat(r.output); !os.IsNotExist(err) {
		t.Fatalf("a refused review published %s", r.output)
	}
}

// The model inspects the bundle through the review_input tools Codex serves
// from the worker executable, and its assessment becomes the published
// report, with the verdict it supports.
func TestReviewSessionPublishesTheModelsAssessment(t *testing.T) {
	head := finding(Location{Side: "head", Path: "parser.go", StartLine: 2, EndLine: 2})
	deleted := finding(Location{Side: "base", Path: "deleted.txt", StartLine: 1, EndLine: 1})
	for name, tc := range map[string]struct {
		complete bool
		findings []Finding
		verdict  string
	}{
		"no findings":       {true, nil, "no_findings"},
		"finding":           {true, []Finding{head}, "findings"},
		"deleted finding":   {true, []Finding{deleted}, "findings"},
		"incomplete review": {false, nil, "incomplete"},
	} {
		t.Run(name, func(t *testing.T) {
			diff := codextest.ToolCall("review_input", "read", map[string]any{"path": "change.diff"})
			r, report, err := runReview(t, codextest.Steps(diff, codextest.Say(assessment(t, tc.complete, tc.findings...)))...)
			if err != nil {
				t.Fatal(err)
			}
			if out, _ := r.model.Output(diff); !strings.Contains(out, "return s[1]") {
				t.Fatalf("the reader did not serve change.diff: %q", out)
			}
			if report.Verdict != tc.verdict || report.Provenance.CodexVersion != "codex-cli "+CodexVersion() || report.Provenance.ModelRequested != model {
				t.Fatalf("report %+v", report)
			}
			for _, name := range []string{"review.json", "review.md"} {
				if _, err := os.Stat(filepath.Join(r.output, name)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Anything that fails the session or its assessment publishes nothing.
func TestReviewSessionPublishesNothingWhenRefused(t *testing.T) {
	foreign := finding(Location{Side: "head", Path: "../auth.json", StartLine: 1, EndLine: 1})
	beyond := finding(Location{Side: "head", Path: "parser.go", StartLine: 999, EndLine: 999})
	for name, tc := range map[string]struct {
		turns  []codextest.Turn
		reason string
	}{
		// Codex reports its own MCP resource tool as a call on server
		// "codex": offered by the pinned release, outside the policy.
		"Codex's own tool": {codextest.Steps(codextest.FunctionCall("list_mcp_resources", map[string]any{}), codextest.Say("unreachable")), "outside the inspection policy"},
		"refused request":  {codextest.Steps(codextest.Fail("usage_not_included", "The plan does not include this model.")), "Codex turn failed"},
		"malformed":        {codextest.Steps(codextest.Say("{")), ""},
		// Codex writes an empty last message for a turn that says nothing.
		"no assessment":      {[]codextest.Turn{func(codextest.Request) []codextest.Item { return nil }}, "invalid review JSON"},
		"path outside input": {codextest.Steps(codextest.Say(assessment(t, true, foreign))), ""},
		"line beyond file":   {codextest.Steps(codextest.Say(assessment(t, true, beyond))), ""},
	} {
		t.Run(name, func(t *testing.T) {
			r, _, err := runReview(t, tc.turns...)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("got %v, want a refusal naming %q", err, tc.reason)
			}
			r.unpublished(t)
		})
	}
}

// A review session cannot change anything: Codex runs read-only and is
// offered neither its file-edit tool nor a shell, so a model that calls
// either anyway gets an unsupported call. Codex reports neither as an item,
// so the review itself stands; what is checked is that nothing ran.
func TestReviewSessionCannotEditOrExecute(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	edit := codextest.Patch("*** Begin Patch\n*** Add File: added.txt\n+x\n*** End Patch")
	shell := codextest.FunctionCall("exec_command", map[string]any{"cmd": "touch " + marker})
	r, report, err := runReview(t, codextest.Steps(edit, shell, codextest.Say(assessment(t, true)))...)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []codextest.Item{edit, shell} {
		if out, _ := r.model.Output(call); !strings.HasPrefix(out, "unsupported") {
			t.Errorf("%s was not refused as unsupported: %s", call["name"], out)
		}
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Error("a shell command ran")
	}
	if after, err := LoadBundle(r.bundle.Dir); err != nil || after.Digest != r.bundle.Digest || report.Provenance.InputDigest != r.bundle.Digest {
		t.Fatalf("the bundle changed: %v", err)
	}
}
