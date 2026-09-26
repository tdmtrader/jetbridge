package implement

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reviewFindings is findings.json as the review client writes it: the
// findings array of a verified review/v1 report, in its published encoding.
const reviewFindings = `[
  {
    "id": "f-001",
    "severity": "high",
    "dimension": "correctness",
    "title": "Incorrect first-byte handling",
    "explanation": "The fixture change no longer returns the first byte.",
    "recommendation": "Preserve first-byte behavior.",
    "location": {"side": "head", "path": "parser.go", "start_line": 2, "end_line": 2}
  }
]
`

// priorChange is a verified change from Run 7 that edits only long.txt, so a
// later session's edits are distinguishable from what it carried forward.
func priorChange(t *testing.T, s *Snapshot) *PriorChange {
	t.Helper()
	base := treeOf(t, s)
	edited := Tree{}
	for p, f := range base {
		edited[p] = f
	}
	edited["link"] = TreeFile{Data: base["link"].Data, Mode: "100644"}
	edited["long.txt"] = TreeFile{Data: append([]byte("prior line\n"), base["long.txt"].Data...), Mode: "100644"}
	patch, changes, err := Diff(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	run := 7
	summary, err := BuildSummary(s, patch, changes, []byte(completeAssessment), Metadata{RunID: &run, ModelRequested: "test-model", CodexVersion: "codex-cli test", Profile: DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	return &PriorChange{Summary: summary, Patch: patch, RunID: &run}
}

// iterationFixture captures a first snapshot, a prior change against it, and
// a second snapshot of the same base carrying that change and review
// findings from Run 3.
func iterationFixture(t *testing.T) (repo string, first, next *Snapshot, prior *PriorChange) {
	t.Helper()
	repo, first = snapshotFixture(t)
	prior = priorChange(t, first)
	next = captureIteration(t, repo, first.Manifest.BaseCommit, prior, reviewed(reviewFindings, 3, first.Manifest.BaseCommit))
	return repo, first, next, prior
}

func captureIteration(t *testing.T, repo, base string, prior *PriorChange, findings *ReviewFindings) *Snapshot {
	t.Helper()
	brief := filepath.Join(t.TempDir(), "brief.md")
	writeTest(t, brief, "Return the first byte.\n")
	s, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: base, Brief: brief, Output: filepath.Join(t.TempDir(), "snapshot"), Prior: prior, Findings: findings})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSnapshotDigestCoversPriorChangeAndFindings(t *testing.T) {
	repo, first, next, prior := iterationFixture(t)
	m := next.Manifest
	if m.PriorPatchDigest == nil || *m.PriorPatchDigest != prior.Summary.PatchDigest || m.PriorRun == nil || *m.PriorRun != 7 ||
		m.FindingsDigest == nil || m.FindingsRun == nil || *m.FindingsRun != 3 {
		t.Fatalf("manifest does not bind the inputs: %+v", m)
	}
	if next.Digest == first.Digest {
		t.Fatal("carrying inputs kept the plain snapshot's digest")
	}
	for _, name := range []string{PriorFile, FindingsFile} {
		if _, err := os.Stat(filepath.Join(next.Dir, name)); err != nil {
			t.Fatalf("%s not sealed: %v", name, err)
		}
	}
	base := first.Manifest.BaseCommit
	digests := map[string]string{next.Digest: "both"}
	for name, s := range map[string]*Snapshot{
		"prior only":      captureIteration(t, repo, base, prior, nil),
		"findings only":   captureIteration(t, repo, base, nil, reviewed(reviewFindings, 3, first.Manifest.BaseCommit)),
		"other findings":  captureIteration(t, repo, base, prior, reviewed(strings.Replace(reviewFindings, "high", "low", 1), 3, base)),
		"other review":    captureIteration(t, repo, base, prior, reviewed(reviewFindings, 4, base)),
		"prior from disk": captureIteration(t, repo, base, &PriorChange{Summary: prior.Summary, Patch: prior.Patch}, reviewed(reviewFindings, 3, first.Manifest.BaseCommit)),
		"same inputs":     captureIteration(t, repo, base, prior, reviewed(reviewFindings, 3, first.Manifest.BaseCommit)),
	} {
		if name == "same inputs" {
			if s.Digest != next.Digest {
				t.Fatal("recapturing the same inputs changed the digest")
			}
			continue
		}
		if other, seen := digests[s.Digest]; seen {
			t.Fatalf("%s has the digest of %s", name, other)
		}
		digests[s.Digest] = name
	}

	var archive bytes.Buffer
	if err := next.WriteRunInputArchive(context.Background(), &archive); err != nil {
		t.Fatal(err)
	}
	received, err := LoadSnapshot(filepath.Join(extract(t, archive.Bytes()), RunInputSnapshotDir))
	if err != nil || received.Digest != next.Digest {
		t.Fatalf("the uploaded snapshot lost its inputs: %v", err)
	}
	if got, err := received.Findings(); err != nil || string(got) != reviewFindings {
		t.Fatalf("uploaded findings: %q %v", got, err)
	}
}

func TestSnapshotRefusesTamperedInputs(t *testing.T) {
	for _, tamper := range []string{"prior", "findings", "missing-findings", "undeclared-prior", "run-without-input", "findings-without-run"} {
		t.Run(tamper, func(t *testing.T) {
			_, first, next, _ := iterationFixture(t)
			dir := next.Dir
			manifest := func(edit func(map[string]any)) {
				data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err != nil {
					t.Fatal(err)
				}
				var m map[string]any
				if err := json.Unmarshal(data, &m); err != nil {
					t.Fatal(err)
				}
				edit(m)
				data, _ = json.Marshal(m)
				writeTest(t, filepath.Join(dir, "manifest.json"), string(data))
			}
			switch tamper {
			case "prior":
				writeTest(t, filepath.Join(dir, PriorFile), "--- a/long.txt\n")
			case "findings":
				writeTest(t, filepath.Join(dir, FindingsFile), "[]\n")
			case "missing-findings":
				os.Remove(filepath.Join(dir, FindingsFile))
			case "undeclared-prior":
				// A plain snapshot cannot gain an input its manifest does not name.
				dir = first.Dir
				data, _ := os.ReadFile(filepath.Join(next.Dir, PriorFile))
				writeTest(t, filepath.Join(dir, PriorFile), string(data))
			case "run-without-input":
				manifest(func(m map[string]any) { delete(m, "prior_patch_digest"); os.Remove(filepath.Join(dir, PriorFile)) })
			case "findings-without-run":
				manifest(func(m map[string]any) { delete(m, "findings_run") })
			}
			if _, err := LoadSnapshot(dir); err == nil {
				t.Fatalf("accepted %s", tamper)
			}
		})
	}
}

func TestSnapshotRefusesAPriorForAnotherBase(t *testing.T) {
	repo, first := snapshotFixture(t)
	prior := priorChange(t, first)
	writeTest(t, filepath.Join(repo, "long.txt"), "moved on\n")
	gitTest(t, repo, "commit", "-qam", "next")
	next := gitTest(t, repo, "rev-parse", "HEAD")
	brief := filepath.Join(t.TempDir(), "brief.md")
	writeTest(t, brief, "Return the first byte.\n")
	capture := func(base string, prior *PriorChange, findings *ReviewFindings) error {
		_, err := CaptureSnapshot(context.Background(), CaptureOptions{Repo: repo, Base: base, Brief: brief, Output: filepath.Join(t.TempDir(), "snapshot"), Prior: prior, Findings: findings})
		return err
	}
	if err := capture(next, prior, nil); err == nil || !strings.Contains(err.Error(), "not the requested base") {
		t.Fatalf("accepted a prior change made against another base: %v", err)
	}
	// The same summary claiming the new base still does not apply there.
	moved := *prior.Summary
	moved.Provenance.BaseCommit = next
	if err := capture(next, &PriorChange{Summary: &moved, Patch: prior.Patch, RunID: prior.RunID}, nil); err == nil || !strings.Contains(err.Error(), "does not apply") {
		t.Fatalf("accepted a prior change that does not apply to the base: %v", err)
	}
	base := first.Manifest.BaseCommit
	other := 8
	for name, c := range map[string]struct {
		prior    *PriorChange
		findings *ReviewFindings
	}{
		"patch not the summary's": {&PriorChange{Summary: prior.Summary, Patch: append([]byte{}, prior.Patch[:len(prior.Patch)-1]...), RunID: prior.RunID}, nil},
		"run not the summary's":   {&PriorChange{Summary: prior.Summary, Patch: prior.Patch, RunID: &other}, nil},
		"findings without a Run":  {nil, reviewed(reviewFindings, 0, base)},
		"empty findings":          {nil, reviewed("[]", 3, base)},
		"findings not an array":   {nil, reviewed(`{"findings":[]}`, 3, base)},
		"findings not objects":    {nil, reviewed(`["a"]`, 3, base)},
		"trailing findings":       {nil, reviewed(reviewFindings+"[]", 3, base)},
		"review of another range": {nil, reviewed(reviewFindings, 3, next)},
	} {
		if err := capture(base, c.prior, c.findings); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	// Findings about work already committed at the base end there.
	ending := reviewed(reviewFindings, 3, base)
	ending.ReviewedBase, ending.ReviewedHead = next, base
	if err := capture(base, nil, ending); err != nil {
		t.Fatalf("refused findings of a review ending at the base: %v", err)
	}
}

// reviewed is findings from review Run run of reviewedBase..<some head>.
func reviewed(findings string, run int, reviewedBase string) *ReviewFindings {
	return &ReviewFindings{JSON: []byte(findings), RunID: run, ReviewedBase: reviewedBase, ReviewedHead: strings.Repeat("e", 40)}
}

func TestWorkspaceServesReadOnlyInputs(t *testing.T) {
	_, _, next, prior := iterationFixture(t)
	inputs, err := next.ReadOnlyInputs()
	if err != nil || string(inputs[PriorInputPath]) != string(prior.Patch) || string(inputs[FindingsInputPath]) != reviewFindings {
		t.Fatalf("inputs %v %v", inputs, err)
	}
	dir := t.TempDir()
	writeTest(t, filepath.Join(dir, "parser.go"), "package parser\n")
	// A repository path that looks like an input is a workspace file, not the input.
	writeTest(t, filepath.Join(dir, "input", FindingsFile), "workspace file\n")
	w, err := NewWorkspaceReader(dir, inputs)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	read := func(name string) string {
		out, err := w.Call("read", json.RawMessage(`{"path":"`+name+`","limit":500}`))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return out.(map[string]any)["text"].(string)
	}
	if got := read(FindingsInputPath); !strings.Contains(got, "2:   {") || !strings.Contains(got, "Incorrect first-byte handling") {
		t.Fatalf("findings read %q", got)
	}
	if got := read(PriorInputPath); !strings.Contains(got, "+prior line") {
		t.Fatalf("prior read %q", got)
	}
	if got := read("input/" + FindingsFile); !strings.Contains(got, "workspace file") {
		t.Fatalf("relative path read %q", got)
	}
	listed, err := w.Call("list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := json.Marshal(listed); !strings.Contains(string(data), `"`+FindingsInputPath+`"`) || !strings.Contains(string(data), `"`+PriorInputPath+`"`) {
		t.Fatalf("list %s", data)
	}
	found, err := w.Call("search", json.RawMessage(`{"text":"first-byte"}`))
	if data, _ := json.Marshal(found); err != nil || !strings.Contains(string(data), FindingsInputPath) {
		t.Fatalf("search %s %v", data, err)
	}
	if _, err := NewWorkspaceReader(dir, map[string][]byte{"/input/auth.json": []byte("x")}); err == nil {
		t.Fatal("served an unknown input")
	}
	// Only a snapshot carrying inputs names the snapshot to the tool server.
	opts := WorkerOptions{Model: "m", ToolsCommand: "/usr/local/bin/jb-review-worker"}
	if args := implementPolicy(opts, "/rt/s/workspace", next.Dir, "", "").Tools.Args; strings.Join(args, " ") != "workspace-tools --root /rt/s/workspace --snapshot "+next.Dir {
		t.Fatalf("tool server args %v", args)
	}
	if text := instructions(next); !strings.Contains(text, PriorInputPath) || !strings.Contains(text, FindingsInputPath) {
		t.Fatalf("instructions do not name the inputs:\n%s", text)
	}
	_, plain := snapshotFixture(t)
	if text := instructions(plain); strings.Contains(text, "/input/") || text != implementInstructions+"\n## Brief\n\n" {
		t.Fatalf("plain instructions changed:\n%s", text)
	}
}

// The session starts from the prior change, the model reads the findings
// through the real workspace tool server, and the published change is
// against the base, carries the prior change forward, and records both Runs.
func TestIterationProvenanceRoundTrip(t *testing.T) {
	provider := buildProvider(t)
	tools := filepath.Join(t.TempDir(), "jb-review-worker")
	if b, err := exec.Command("go", "build", "-o", tools, "github.com/concourse/concourse/cmd/jb-review-worker").CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v: %s", err, b)
	}
	repo, _, next, _ := iterationFixture(t)
	output := filepath.Join(t.TempDir(), "change")
	run := 8
	summary, err := runInRuntime(context.Background(), WorkerOptions{
		Input: next.Dir, Output: output, RuntimeDir: t.TempDir(), Codex: provider, ToolsCommand: tools,
		Model: "edit", Auth: io.NopCloser(strings.NewReader(syntheticAuth)), Timeout: 20 * time.Second, RunID: &run,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary.Summary, "Addressed review finding: Incorrect first-byte handling") {
		t.Fatalf("the model did not read the findings through the workspace tools: %q", summary.Summary)
	}
	p := summary.Provenance
	if p.PriorRun == nil || *p.PriorRun != 7 || p.FindingsRun == nil || *p.FindingsRun != 3 || p.InputDigest != next.Digest {
		t.Fatalf("provenance %+v", p)
	}
	raw, err := os.ReadFile(filepath.Join(output, SummaryFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"prior_run": 7`)) || !bytes.Contains(raw, []byte(`"findings_run": 3`)) {
		t.Fatalf("summary.json does not record the Runs:\n%s", raw)
	}
	read, patch, err := ReadResult(output)
	if err != nil || *read.Provenance.PriorRun != 7 || *read.Provenance.FindingsRun != 3 {
		t.Fatalf("published summary does not round-trip: %v", err)
	}
	if _, err := ParseChange(raw, patch, next); err != nil {
		t.Fatal(err)
	}
	for field, forged := range map[string]string{`"findings_run": 3`: `"findings_run": 4`, `"prior_run": 7,`: ``} {
		if _, err := ParseChange(bytes.Replace(raw, []byte(field), []byte(forged), 1), patch, next); err == nil {
			t.Errorf("accepted forged provenance %s -> %s", field, forged)
		}
	}
	// The patch is against the base and carries the prior change forward.
	applied, err := Apply(context.Background(), ApplyOptions{Repo: repo, ResultDir: output})
	if err != nil {
		t.Fatal(err)
	}
	if got := gitTest(t, repo, "diff", "--name-status", next.Manifest.BaseCommit, applied.Commit); got != "D\tdeleted.txt\nM\tlong.txt\nM\tparser.go\nA\tparser_test.go" {
		t.Fatalf("applied change:\n%s", got)
	}
}
