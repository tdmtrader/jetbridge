package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
	"github.com/google/uuid"
)

// sealedSnapshot writes a one-file snapshot without Git: the manifest, the
// brief and the base tree, exactly as capture lays them out.
func sealedSnapshot(t *testing.T) *implement.Snapshot {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "snapshot")
	base := "package parser\nfunc First(s string) byte { return s[1] }\n"
	brief := "Return the first byte.\n"
	if err := os.MkdirAll(filepath.Join(dir, "base"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"base/parser.go": base, "brief.md": brief} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := implement.Manifest{
		Version: implement.InputVersion, BaseCommit: strings.Repeat("a", 40), BriefDigest: capture.Digest([]byte(brief)),
		Files: []capture.File{{Side: "base", Path: "parser.go", Mode: "100644", Digest: capture.Digest([]byte(base)), Size: int64(len(base))}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := implement.LoadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// publishedChange builds a change the way the worker publishes it.
func publishedChange(t *testing.T, s *implement.Snapshot, run int) (summary, patch []byte) {
	t.Helper()
	base := implement.Tree{"parser.go": {Data: []byte("package parser\nfunc First(s string) byte { return s[1] }\n"), Mode: "100644"}}
	edited := implement.Tree{"parser.go": {Data: []byte("package parser\nfunc First(s string) byte { return s[0] }\n"), Mode: "100644"}}
	patch, changes, err := implement.Diff(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	built, err := implement.BuildSummary(s, patch, changes, []byte(`{"summary":"Return the first byte.","complete":true,"limitations":[]}`),
		implement.Metadata{RunID: &run, ModelRequested: "test-model", CodexVersion: "codex-cli test", Profile: implement.DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	summary, err = json.MarshalIndent(built, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(summary, '\n'), patch
}

func TestMain(m *testing.M) { os.Exit(atctest.Run(m)) }

// The two installed templates: the implement template as deployed, and the
// same template before it had a validate task.
const (
	template   = "implement"
	changeOnly = "implement-change-only"
)

// platform is the real platform, seen by one test through its own team, with
// both templates installed.
type platform struct {
	*atctest.Platform
	team string
}

func newPlatform(t *testing.T) *platform {
	t.Helper()
	p := &platform{Platform: atctest.Get(t)}
	p.team = p.NewTeam(t)
	p.Install(t, p.team, template, installedTemplate(t))
	withoutValidation := installedTemplate(t)
	withoutValidation.Jobs[0].PlanSequence = withoutValidation.Jobs[0].PlanSequence[:1]
	p.Install(t, p.team, changeOnly, withoutValidation)
	return p
}

func (p *platform) client(t *testing.T) *Client {
	c, err := New(p.URL, p.Client(t))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// admit uploads s and admits a Run of name with it, as a submission does
// before its credential handoff.
func (p *platform) admit(t *testing.T, name string, s *implement.Snapshot) Handle {
	t.Helper()
	ctx := context.Background()
	c := p.client(t)
	reader, writer := io.Pipe()
	go func() { writer.CloseWithError(s.WriteRunInputArchive(ctx, writer)) }()
	source, err := c.UploadInput(ctx, p.team, name, SnapshotInput, reader)
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.CreateRun(ctx, p.team, name, atc.CreatePipelineRunV2Request{InvocationKey: uuid.NewString(), Inputs: map[string]atc.RunInputSource{SnapshotInput: source}})
	if err != nil {
		t.Fatal(err)
	}
	return Handle{Team: p.team, Template: name, Number: run.Number}
}

// runID is the ID of the Run handle names.
func (p *platform) runID(t *testing.T, handle Handle) int {
	t.Helper()
	run, err := p.client(t).Status(context.Background(), handle)
	if err != nil {
		t.Fatal(err)
	}
	return run.ID
}

// publish makes the Run succeed with each named result's files.
func (p *platform) publish(t *testing.T, handle Handle, results map[string]map[string][]byte) {
	t.Helper()
	p.Succeed(t, handle.Team, handle.Template, handle.Number, results)
}

func TestSubmitUploadsTheSnapshotAndHandsOffToTheChangeProducer(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	root := t.TempDir()
	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"synthetic":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, "request.json")
	p.Schedule(t, p.team, template, ChangeResult)
	// Only the change producer is started: a handoff to any other result's
	// producer would wait, so the submission is bounded.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	result, err := p.client(t).Submit(ctx, SubmitOptions{Team: p.team, Template: template, Input: s.Dir, Receipt: receipt, AuthFile: auth})
	if err != nil || !result.Ready || result.RunID < 1 {
		t.Fatalf("submission did not reach readiness: %+v %v", result, err)
	}
	if delivered, attempts := p.Delivered(result.RunID); string(delivered) != `{"synthetic":true}` || attempts != 1 {
		t.Fatalf("credentials were not handed to the change producer: %q after %d deliveries", delivered, attempts)
	}
	// The Run's input is the sealed snapshot under snapshot/, byte for byte.
	input := p.Input(t, p.team, template, result.Handle.Number, SnapshotInput)
	names := map[string]bool{}
	if err := filepath.WalkDir(input, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		// The node's own materialization marker is not part of the input.
		if relative, _ := filepath.Rel(input, path); relative != ".hangar-materialized" {
			names[filepath.ToSlash(relative)] = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 || !names["snapshot/manifest.json"] || !names["snapshot/brief.md"] || !names["snapshot/base/parser.go"] {
		t.Fatalf("the upload is not the sealed snapshot under snapshot/: %v", names)
	}
	if received, err := implement.LoadSnapshot(filepath.Join(input, "snapshot")); err != nil || received.Digest != s.Digest {
		t.Fatalf("the Run received another snapshot: %v", err)
	}
	data, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["workload"] != "implement" || fields["input_digest"] != s.Digest {
		t.Fatalf("the receipt does not name the implement workload and snapshot: %s", data)
	}
}

func TestResultReturnsTheVerifiedChange(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	handle := p.admit(t, changeOnly, s)
	runID := p.runID(t, handle)
	summary, patch := publishedChange(t, s, runID)
	p.publish(t, handle, map[string]map[string][]byte{ChangeResult: {implement.SummaryFile: summary, implement.PatchFile: patch}})
	change, err := p.client(t).Result(context.Background(), handle, ChangeResult)
	if err != nil {
		t.Fatal(err)
	}
	if change.RunID() != runID || change.Patch != string(patch) || change.Summary.Provenance.InputDigest != s.Digest || len(change.Summary.ChangedFiles) != 1 {
		t.Fatalf("unexpected change: %+v", change.Summary)
	}
	if !strings.Contains(change.Markdown(), "```diff\n"+string(patch)) {
		t.Fatal("markdown does not carry the patch")
	}
	// The change is written back exactly as published and applies locally
	// the same way a change from the worker's own output does.
	output := filepath.Join(t.TempDir(), "change")
	if err := change.WriteDir(output); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]byte{implement.SummaryFile: summary, implement.PatchFile: patch} {
		got, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("%s was not written as published: %v", name, err)
		}
	}
	if err := change.WriteDir(output); err == nil {
		t.Fatal("a retrieved change overwrote an existing directory")
	}
}

func TestResultRefusals(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	for name, c := range map[string]struct {
		files func(runID int) map[string][]byte
		want  string
	}{
		"run_id mismatch": {func(runID int) map[string][]byte {
			other, patch := publishedChange(t, s, runID+1)
			return map[string][]byte{implement.SummaryFile: other, implement.PatchFile: patch}
		}, "different Run"},
		"patch digest": {func(runID int) map[string][]byte {
			summary, patch := publishedChange(t, s, runID)
			return map[string][]byte{implement.SummaryFile: summary, implement.PatchFile: append([]byte("\n"), patch...)}
		}, "patch digest"},
		"missing patch": {func(runID int) map[string][]byte {
			summary, _ := publishedChange(t, s, runID)
			return map[string][]byte{implement.SummaryFile: summary}
		}, implement.PatchFile},
		"missing summary": {func(runID int) map[string][]byte {
			_, patch := publishedChange(t, s, runID)
			return map[string][]byte{implement.PatchFile: patch}
		}, implement.SummaryFile},
		"a change outside Run": {func(runID int) map[string][]byte {
			summary, patch := publishedChange(t, s, runID)
			var built implement.Summary
			if err := json.Unmarshal(summary, &built); err != nil {
				t.Fatal(err)
			}
			built.RunID = nil
			local, err := json.Marshal(built)
			if err != nil {
				t.Fatal(err)
			}
			return map[string][]byte{implement.SummaryFile: local, implement.PatchFile: patch}
		}, "does not name the Run"},
		"malformed summary": {func(runID int) map[string][]byte {
			_, patch := publishedChange(t, s, runID)
			return map[string][]byte{implement.SummaryFile: []byte("{"), implement.PatchFile: patch}
		}, "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			handle := p.admit(t, changeOnly, s)
			p.publish(t, handle, map[string]map[string][]byte{ChangeResult: c.files(p.runID(t, handle))})
			change, err := p.client(t).Result(context.Background(), handle, ChangeResult)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("change was not refused with %q: %+v %v", c.want, change, err)
			}
		})
	}
	t.Run("a change not retrieved from a Run is not written", func(t *testing.T) {
		if err := (&Change{Summary: &implement.Summary{}}).WriteDir(filepath.Join(t.TempDir(), "change")); err == nil {
			t.Fatal("an unverified change was written")
		}
	})
}

func TestWorkloadIsFixed(t *testing.T) {
	w := Workload()
	w.Name = "review"
	if again := Workload(); again.Name != "implement" || again.Input != SnapshotInput || again.CredentialResult != ChangeResult || again.Load == nil {
		t.Fatalf("the implement workload changed: %+v", again)
	}
}

// publishedValidation is validation.json as the template's validate task
// writes it for a change.
func publishedValidation(t *testing.T, summary, patch []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var s implement.Summary
	if err := json.Unmarshal(summary, &s); err != nil {
		t.Fatal(err)
	}
	v := map[string]any{
		"schema_version": implement.ValidationVersion, "run_id": *s.RunID,
		"input_digest": s.Provenance.InputDigest, "patch_digest": capture.Digest(patch),
		"applied": true, "command": "go test ./...", "exit_code": 1, "outcome": "failed",
		"log_tail": "--- FAIL: TestFirst\nFAIL\n",
	}
	if edit != nil {
		edit(v)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

// validatedRun admits a Run of the installed template and publishes its
// change and that change's validation, edited by edit.
func (p *platform) validatedRun(t *testing.T, s *implement.Snapshot, edit func(map[string]any)) Handle {
	t.Helper()
	handle := p.admit(t, template, s)
	summary, patch := publishedChange(t, s, p.runID(t, handle))
	p.publish(t, handle, map[string]map[string][]byte{
		ChangeResult:     {implement.SummaryFile: summary, implement.PatchFile: patch},
		ValidationResult: {implement.ValidationFile: publishedValidation(t, summary, patch, edit)},
	})
	return handle
}

func TestValidationIsBoundToTheRunsChange(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	handle := p.validatedRun(t, s, nil)
	c := p.client(t)
	change, err := c.Result(context.Background(), handle, ChangeResult)
	if err != nil {
		t.Fatal(err)
	}
	validation, err := c.Validation(context.Background(), handle, change)
	if err != nil {
		t.Fatal(err)
	}
	// A failing command is a result, not an error: the Run succeeded and
	// both the change and its failing validation are retrievable.
	if validation.Outcome != implement.ValidationFailed || validation.ExitCode == nil || *validation.ExitCode != 1 || validation.PatchDigest != change.Summary.PatchDigest {
		t.Fatalf("unexpected validation: %+v", validation)
	}
	markdown := change.ValidatedMarkdown(validation)
	if !strings.Contains(markdown, "## Validation: failed") || !strings.Contains(markdown, "--- FAIL: TestFirst") || strings.Index(markdown, "## Validation") > strings.Index(markdown, "## Patch") {
		t.Fatalf("markdown does not show the validation before the patch:\n%s", markdown)
	}
	if _, err := c.Validation(context.Background(), handle, &Change{Summary: change.Summary, Patch: change.Patch}); err == nil {
		t.Fatal("a validation was checked against a change not retrieved from a Run")
	}
}

func TestValidationRefusals(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	for name, c := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"another patch":      {func(v map[string]any) { v["patch_digest"] = strings.Repeat("0", 64) }, "patch digest"},
		"another snapshot":   {func(v map[string]any) { v["input_digest"] = strings.Repeat("0", 64) }, "different snapshot"},
		"another Run":        {func(v map[string]any) { v["run_id"] = v["run_id"].(int) + 1 }, "different Run"},
		"passed with a fail": {func(v map[string]any) { v["outcome"] = "passed" }, "exit code"},
		"not applied but ran": {func(v map[string]any) {
			v["outcome"], v["applied"] = "not_applied", false
		}, "did not apply"},
		"unknown field": {func(v map[string]any) { v["extra"] = true }, "schema"},
	} {
		t.Run(name, func(t *testing.T) {
			handle := p.validatedRun(t, s, c.edit)
			client := p.client(t)
			change, err := client.Result(context.Background(), handle, ChangeResult)
			if err != nil {
				t.Fatal(err)
			}
			validation, err := client.Validation(context.Background(), handle, change)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("validation was not refused with %q: %+v %v", c.want, validation, err)
			}
		})
	}
	t.Run("a Run without a validate task", func(t *testing.T) {
		handle := p.admit(t, changeOnly, s)
		summary, patch := publishedChange(t, s, p.runID(t, handle))
		p.publish(t, handle, map[string]map[string][]byte{ChangeResult: {implement.SummaryFile: summary, implement.PatchFile: patch}})
		client := p.client(t)
		change, err := client.Result(context.Background(), handle, ChangeResult)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Validation(context.Background(), handle, change); !errors.Is(err, ErrNoValidation) {
			t.Fatalf("a Run without validation was not reported as such: %v", err)
		}
	})
}
