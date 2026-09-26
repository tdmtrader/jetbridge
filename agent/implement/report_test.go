package implement

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const completeAssessment = `{"summary":"Return the first byte.","complete":true,"limitations":[]}`

// publish builds a verified change from the fixture edit and writes it the
// way the worker does.
func publish(t *testing.T, s *Snapshot, runID *int) (string, *Summary, []byte) {
	t.Helper()
	base := treeOf(t, s)
	edited := edit(base)
	edited["link"] = TreeFile{Data: base["link"].Data, Mode: "100644"}
	patch, changes, err := Diff(base, edited)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := BuildSummary(s, patch, changes, []byte(completeAssessment), Metadata{RunID: runID, ModelRequested: "test-model", CodexVersion: "codex-cli test", Profile: DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "change")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	writeTest(t, filepath.Join(dir, SummaryFile), string(data)+"\n")
	writeTest(t, filepath.Join(dir, PatchFile), string(patch))
	return dir, summary, patch
}

func TestSummaryIsStampedByTheWorker(t *testing.T) {
	_, s := snapshotFixture(t)
	run := 7
	dir, summary, patch := publish(t, s, &run)
	if summary.SchemaVersion != "implement/v1" || *summary.RunID != 7 || summary.Provenance.InputDigest != s.Digest ||
		summary.Provenance.BaseCommit != s.Manifest.BaseCommit || summary.Provenance.ExecutionPolicy != "edit-only" || summary.Provenance.Provider != "codex" {
		t.Fatalf("provenance not stamped: %+v", summary)
	}
	want := []ChangedFile{{"deleted.txt", "deleted"}, {"parser.go", "modified"}, {"parser_test.go", "added"}}
	if len(summary.ChangedFiles) != len(want) {
		t.Fatalf("changed files %v", summary.ChangedFiles)
	}
	for i := range want {
		if summary.ChangedFiles[i] != want[i] {
			t.Fatalf("changed files %v", summary.ChangedFiles)
		}
	}
	if !strings.Contains(strings.Join(summary.Limitations, " "), "not executed") {
		t.Fatal("missing non-execution disclosure")
	}
	read, readPatch, err := ReadResult(dir)
	if err != nil || read.PatchDigest != summary.PatchDigest || string(readPatch) != string(patch) {
		t.Fatalf("published change does not read back: %v", err)
	}
	data, _ := json.Marshal(summary)
	if _, err := ParseChange(data, patch, s); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyChangeIsIncomplete(t *testing.T) {
	_, s := snapshotFixture(t)
	summary, err := BuildSummary(s, nil, nil, []byte(completeAssessment), Metadata{ModelRequested: "m", CodexVersion: "v", Profile: DefaultProfile()})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Complete || len(summary.ChangedFiles) != 0 || !strings.Contains(strings.Join(summary.Limitations, " "), "No files were changed") {
		t.Fatalf("empty change not disclosed: %+v", summary)
	}
}

func TestRejectInvalidAssessmentsAndChanges(t *testing.T) {
	_, s := snapshotFixture(t)
	for name, raw := range map[string]string{
		"malformed": "{", "unknown": `{"summary":"x","complete":true,"limitations":[],"changed_files":[]}`,
		"missing": `{"summary":"x","limitations":[]}`, "blank": `{"summary":"  ","complete":true,"limitations":[]}`,
	} {
		if _, err := BuildSummary(s, nil, nil, []byte(raw), Metadata{ModelRequested: "m", CodexVersion: "v", Profile: DefaultProfile()}); err == nil {
			t.Errorf("accepted %s assessment", name)
		}
	}
	_, summary, patch := publish(t, s, nil)
	good, _ := json.Marshal(summary)
	mutate := func(f func(*Summary)) []byte {
		var copy Summary
		json.Unmarshal(good, &copy)
		f(&copy)
		b, _ := json.Marshal(copy)
		return b
	}
	for name, c := range map[string]struct{ summary, patch []byte }{
		"patch-digest":  {good, append(append([]byte{}, patch...), []byte("diff --git a/zz b/zz\nnew file mode 100644\n")...)},
		"changed-files": {mutate(func(x *Summary) { x.ChangedFiles = x.ChangedFiles[:1] }), patch},
		"wrong-status":  {mutate(func(x *Summary) { x.ChangedFiles[0].Status = "modified" }), patch},
		"no-disclosure": {mutate(func(x *Summary) { x.Limitations = []string{} }), patch},
		"policy":        {mutate(func(x *Summary) { x.Provenance.ExecutionPolicy = "inspect-only" }), patch},
		"empty-complete": {mutate(func(x *Summary) {
			x.ChangedFiles = []ChangedFile{}
			x.PatchDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
		}), []byte{}},
		"trailing-json": {append(append([]byte{}, good...), []byte(" {}")...), patch},
	} {
		if _, err := ParsePublishedChange(c.summary, c.patch); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	other := mutate(func(x *Summary) { x.Provenance.InputDigest = strings.Repeat("0", 64) })
	if _, err := ParseChange(other, patch, s); err == nil {
		t.Error("accepted a change from another snapshot")
	}
}

func TestReadResultRequiresExactlyTwoFiles(t *testing.T) {
	_, s := snapshotFixture(t)
	dir, _, _ := publish(t, s, nil)
	writeTest(t, filepath.Join(dir, "extra"), "x")
	if _, _, err := ReadResult(dir); err == nil {
		t.Fatal("accepted an extra file")
	}
}
