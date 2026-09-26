package client

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/capture"
	"github.com/concourse/concourse/agent/implement"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar"
)

const (
	team     = "main"
	template = "implement"
	runID    = 41
	number   = 3
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

// platform serves the public Run routes for one implement Run.
type platform struct {
	server *httptest.Server
	mu     sync.Mutex
	run    atc.PipelineRun
	// archive is the published change result.
	archive []byte
	// uploads records each input name and archive received.
	uploads     map[string][]byte
	credentials []string
}

func newPlatform(t *testing.T) *platform {
	p := &platform{uploads: map[string][]byte{}}
	p.run = atc.PipelineRun{ID: runID, Number: number, ContractVersion: atc.RunContractV2, ActivationEpoch: 1, Status: atc.RunStatusRunning}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}

func (p *platform) client(t *testing.T) *Client {
	c, err := New(p.server.URL, p.server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (p *platform) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v1 := "/api/v1/teams/" + team + "/pipelines/" + template
	v2 := "/api/v2/teams/" + team + "/pipelines/" + template
	run := "/runs/" + strconv.Itoa(number)
	reply := func(value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, v1+"/run-inputs/"):
		body, _ := io.ReadAll(r.Body)
		p.uploads[strings.TrimPrefix(r.URL.Path, v1+"/run-inputs/")] = body
		w.WriteHeader(http.StatusCreated)
		reply(atc.RunInputSource{SourceID: "input-v1-" + strings.Repeat("ab", 32), Bearer: "transient-bearer"})
	case r.Method == http.MethodPost && r.URL.Path == v2+"/runs":
		w.WriteHeader(http.StatusCreated)
		reply(p.run)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, v2+run+"/credentials/"):
		result := strings.TrimPrefix(r.URL.Path, v2+run+"/credentials/")
		p.credentials = append(p.credentials, result)
		reply(atc.RunCredentialSession{RunID: runID, Result: result, Status: "ready"})
	case r.Method == http.MethodGet && r.URL.Path == v1+run:
		reply(p.run)
	case r.Method == http.MethodGet && r.URL.Path == v1+run+"/results/"+ChangeResult:
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Length", strconv.Itoa(len(p.archive)))
		_, _ = w.Write(p.archive)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// publish makes the Run succeed with files as its change result, bound the
// way the platform binds a captured output.
func (p *platform) publish(t *testing.T, files map[string][]byte) {
	t.Helper()
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	tree, err := (hangar.Canonicalizer{MaxContentBytes: 1 << 20, MaxEntries: 32}).Capture(ctx, &raw)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	archive, err := os.ReadFile(tree.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.archive = archive
	p.run.Status = atc.RunStatusSucceeded
	p.run.Terminal = &atc.RunTerminalResult{Status: atc.RunStatusSucceeded, Results: map[string]atc.RunResultBinding{ChangeResult: {Ref: hangar.TreeRef{Digest: tree.Digest}}}}
}

func handle() Handle { return Handle{Team: team, Template: template, Number: number} }

func TestSubmitUploadsTheSnapshotAndHandsOffToTheChangeProducer(t *testing.T) {
	p := newPlatform(t)
	s := sealedSnapshot(t)
	root := t.TempDir()
	auth := filepath.Join(root, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"synthetic":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(root, "request.json")
	result, err := p.client(t).Submit(context.Background(), SubmitOptions{Team: team, Template: template, Input: s.Dir, Receipt: receipt, AuthFile: auth})
	if err != nil || !result.Ready || result.RunID != runID {
		t.Fatalf("submission did not reach readiness: %+v %v", result, err)
	}
	if len(p.uploads) != 1 || p.uploads[SnapshotInput] == nil {
		t.Fatalf("the snapshot was not uploaded as %q: %v", SnapshotInput, p.uploads)
	}
	names := map[string]bool{}
	r := tar.NewReader(bytes.NewReader(p.uploads[SnapshotInput]))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	if len(names) != 3 || !names["snapshot/manifest.json"] || !names["snapshot/brief.md"] || !names["snapshot/base/parser.go"] {
		t.Fatalf("the upload is not the sealed snapshot under snapshot/: %v", names)
	}
	if len(p.credentials) == 0 || p.credentials[0] != ChangeResult {
		t.Fatalf("credentials were not handed to the change producer: %v", p.credentials)
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
	summary, patch := publishedChange(t, s, runID)
	p.publish(t, map[string][]byte{implement.SummaryFile: summary, implement.PatchFile: patch})
	change, err := p.client(t).Result(context.Background(), handle(), ChangeResult)
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
	s := sealedSnapshot(t)
	summary, patch := publishedChange(t, s, runID)
	other, _ := publishedChange(t, s, runID+1)
	local, localPatch := func() ([]byte, []byte) {
		var built implement.Summary
		if err := json.Unmarshal(summary, &built); err != nil {
			t.Fatal(err)
		}
		built.RunID = nil
		data, err := json.Marshal(built)
		if err != nil {
			t.Fatal(err)
		}
		return data, patch
	}()
	for name, c := range map[string]struct {
		files map[string][]byte
		want  string
	}{
		"run_id mismatch":      {map[string][]byte{implement.SummaryFile: other, implement.PatchFile: patch}, "different Run"},
		"patch digest":         {map[string][]byte{implement.SummaryFile: summary, implement.PatchFile: append([]byte("\n"), patch...)}, "patch digest"},
		"missing patch":        {map[string][]byte{implement.SummaryFile: summary}, implement.PatchFile},
		"missing summary":      {map[string][]byte{implement.PatchFile: patch}, implement.SummaryFile},
		"a change outside Run": {map[string][]byte{implement.SummaryFile: local, implement.PatchFile: localPatch}, "does not name the Run"},
		"malformed summary":    {map[string][]byte{implement.SummaryFile: []byte("{"), implement.PatchFile: patch}, "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPlatform(t)
			p.publish(t, c.files)
			change, err := p.client(t).Result(context.Background(), handle(), ChangeResult)
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
