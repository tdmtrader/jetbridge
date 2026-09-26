package implement

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/capture"
)

func validationJSON(t *testing.T, edit func(map[string]any)) []byte {
	t.Helper()
	v := map[string]any{
		"schema_version": ValidationVersion, "run_id": 8,
		"input_digest": strings.Repeat("a", 64), "patch_digest": capture.Digest([]byte("patch")),
		"applied": true, "command": "go test ./...", "exit_code": 0, "outcome": "passed", "log_tail": "ok\n",
	}
	if edit != nil {
		edit(v)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseValidationOutcomes(t *testing.T) {
	for name, c := range map[string]struct {
		edit func(map[string]any)
		want string // empty: accepted
	}{
		"passed": {nil, ""},
		"failed": {func(v map[string]any) { v["exit_code"], v["outcome"] = 2, "failed" }, ""},
		"not applied": {func(v map[string]any) {
			v["applied"], v["exit_code"], v["outcome"] = false, nil, "not_applied"
		}, ""},
		"passed with a nonzero exit": {func(v map[string]any) { v["exit_code"] = 1 }, "exit code"},
		"failed with a zero exit":    {func(v map[string]any) { v["outcome"] = "failed" }, "exit code"},
		"ran without applying":       {func(v map[string]any) { v["applied"] = false }, "without applying"},
		"no exit code":               {func(v map[string]any) { v["exit_code"] = nil }, "without applying"},
		"not applied with an exit":   {func(v map[string]any) { v["applied"], v["outcome"] = false, "not_applied" }, "did not apply"},
		"not applied but applied": {func(v map[string]any) {
			v["exit_code"], v["outcome"] = nil, "not_applied"
		}, "did not apply"},
		"unknown outcome":   {func(v map[string]any) { v["outcome"] = "skipped" }, "schema"},
		"no Run":            {func(v map[string]any) { v["run_id"] = nil }, "schema"},
		"short digest":      {func(v map[string]any) { v["patch_digest"] = "abc" }, "schema"},
		"empty command":     {func(v map[string]any) { v["command"] = "" }, "schema"},
		"extra field":       {func(v map[string]any) { v["agent_says"] = "passed" }, "schema"},
		"exit out of range": {func(v map[string]any) { v["exit_code"], v["outcome"] = 256, "failed" }, "schema"},
		"another version":   {func(v map[string]any) { v["schema_version"] = "implement-validation/v2" }, "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			v, err := ParseValidation(validationJSON(t, c.edit))
			if c.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("validation was not refused with %q: %+v %v", c.want, v, err)
			}
		})
	}
	if _, err := ParseValidation(append(validationJSON(t, nil), []byte("{}")...)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if _, err := ParseValidation(bytes.Repeat([]byte(" "), MaxValidationBytes+1)); err == nil {
		t.Fatal("an oversized validation was accepted")
	}
}

// The log tail is the command's raw output cut at a byte boundary, so
// invalid UTF-8 is replaced rather than refused.
func TestParseValidationSanitizesTheLogTail(t *testing.T) {
	data := bytes.Replace(validationJSON(t, nil), []byte(`"ok\n"`), []byte("\"\xe9t\xc3\""), 1)
	v, err := ParseValidation(data)
	if err != nil {
		t.Fatal(err)
	}
	if v.LogTail != "�t�" {
		t.Fatalf("log tail was not sanitized: %q", v.LogTail)
	}
}

func TestValidationAttestsOnlyItsChange(t *testing.T) {
	patch := []byte("patch")
	run := 8
	summary := &Summary{RunID: &run, Provenance: Provenance{InputDigest: strings.Repeat("a", 64)}, PatchDigest: capture.Digest(patch)}
	v, err := ParseValidation(validationJSON(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Attests(summary, patch); err != nil {
		t.Fatal(err)
	}
	other := 9
	for name, c := range map[string]struct {
		summary Summary
		patch   []byte
	}{
		"another Run":      {Summary{RunID: &other, Provenance: summary.Provenance, PatchDigest: summary.PatchDigest}, patch},
		"no Run":           {Summary{Provenance: summary.Provenance, PatchDigest: summary.PatchDigest}, patch},
		"another snapshot": {Summary{RunID: &run, Provenance: Provenance{InputDigest: strings.Repeat("b", 64)}, PatchDigest: summary.PatchDigest}, patch},
		"another patch":    {Summary{RunID: &run, Provenance: summary.Provenance, PatchDigest: summary.PatchDigest}, []byte("other")},
	} {
		if err := v.Attests(&c.summary, c.patch); err == nil {
			t.Fatalf("%s: validation attested a change it did not run against", name)
		}
	}
}

func TestValidationMarkdownFencesUntrustedText(t *testing.T) {
	v, err := ParseValidation(validationJSON(t, func(v map[string]any) {
		v["exit_code"], v["outcome"], v["log_tail"] = 1, "failed", "```\n# not a heading\n"
	}))
	if err != nil {
		t.Fatal(err)
	}
	markdown := v.Markdown()
	if !strings.HasPrefix(markdown, "# Validation: failed\n") || !strings.Contains(markdown, "Exit code: 1") || !strings.Contains(markdown, "````text\n```\n# not a heading\n````\n") {
		t.Fatalf("unexpected markdown:\n%s", markdown)
	}
	notApplied, err := ParseValidation(validationJSON(t, func(v map[string]any) {
		v["applied"], v["exit_code"], v["outcome"] = false, nil, "not_applied"
	}))
	if err != nil {
		t.Fatal(err)
	}
	if markdown := notApplied.Markdown(); !strings.Contains(markdown, "# Validation: not applied") || strings.Contains(markdown, "Exit code") {
		t.Fatalf("unexpected markdown:\n%s", markdown)
	}
}
