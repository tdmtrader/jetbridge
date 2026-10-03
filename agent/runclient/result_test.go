package runclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/atctest"
	"github.com/concourse/concourse/hangar"
)

type parsedResult struct{ runID int }

func (p parsedResult) RunID() int { return p.runID }

func parseRunID(tree fs.FS) (RunIdentified, error) {
	body, err := ReadResultFile(tree, "result.json", 1<<10)
	if err != nil {
		return nil, err
	}
	var value struct {
		RunID int `json:"run_id"`
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, err
	}
	return parsedResult{value.RunID}, nil
}

const resultTemplate = "results"

// newResultPlatform installs a template whose Runs take no input and publish
// one result, outcome.
func newResultPlatform(t *testing.T) *platform {
	p := &platform{Platform: atctest.Get(t)}
	p.team, p.url = p.NewTeam(t), p.URL
	p.Install(t, p.team, resultTemplate, atc.Config{Jobs: atc.JobConfigs{{Name: "work", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
		Name: "produce", TaskID: "4b7e2a91-3c6d-4f18-8e5a-0d9c2b1a7f63", RunResult: &atc.RunResult{Name: "outcome", Output: "out"},
		Config: &atc.TaskConfig{Platform: "linux", RootfsURI: "docker:///" + atctest.Pin, Outputs: []atc.TaskOutputConfig{{Name: "out"}},
			Run: atc.TaskRunConfig{Path: "true"}},
	}}}}}})
	return p
}

// succeeded admits a Run that succeeds with files as its outcome result.
// Each file's content may name the Run ID with %d.
func (p *platform) succeeded(t *testing.T, files map[string]string) (atc.PipelineRun, Handle) {
	run := p.Admit(t, p.team, resultTemplate)
	published := map[string][]byte{}
	for name, content := range files {
		if strings.Contains(content, "%d") {
			content = fmt.Sprintf(content, run.ID)
		}
		published[name] = []byte(content)
	}
	p.Succeed(t, p.team, resultTemplate, run.Number, map[string]map[string][]byte{"outcome": published})
	return run, Handle{Team: p.team, Template: resultTemplate, Number: run.Number}
}

func TestResultVerifiesTheBindingThenTheRun(t *testing.T) {
	p := newResultPlatform(t)
	run, handle := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d}`})
	parsed, err := p.client(t).Result(context.Background(), handle, "outcome", parseRunID)
	if err != nil || parsed.RunID() != run.ID {
		t.Fatalf("verified result was refused: %v %v", parsed, err)
	}
}

// The platform never serves an archive its binding does not name, so the
// mismatch is put on the wire by a decorator rewriting the Run's answer.
func TestResultRefusesADigestMismatchBeforeParsing(t *testing.T) {
	p := newResultPlatform(t)
	_, handle := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d}`})
	runPath := "/api/v1/teams/" + p.team + "/pipelines/" + resultTemplate + "/runs/" + strconv.Itoa(handle.Number)
	p.decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path != runPath {
			next.ServeHTTP(w, r)
			return
		}
		recorder := httptest.NewRecorder()
		r.Header.Del("Accept-Encoding")
		next.ServeHTTP(recorder, r)
		var run atc.PipelineRun
		if err := json.Unmarshal(recorder.Body.Bytes(), &run); err != nil || run.Terminal == nil {
			t.Errorf("the Run's answer: %v", err)
			return
		}
		binding := run.Terminal.Results["outcome"]
		binding.Ref.Digest = hangar.Digest("sha256:" + strings.Repeat("0", 64))
		run.Terminal.Results["outcome"] = binding
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(run)
	})
	called := false
	_, err := p.client(t).Result(context.Background(), handle, "outcome", func(tree fs.FS) (RunIdentified, error) {
		called = true
		return parseRunID(tree)
	})
	if err == nil || !strings.Contains(err.Error(), "retained binding") || called {
		t.Fatalf("a mismatched archive reached the parser: called=%v err=%v", called, err)
	}
}

// Another Run's real published archive, served in place of this Run's.
func TestResultRefusesAnArchiveFromAnotherResult(t *testing.T) {
	p := newResultPlatform(t)
	_, handle := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d}`})
	_, other := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d, "extra":1}`})
	prefix := "/api/v1/teams/" + p.team + "/pipelines/" + resultTemplate + "/runs/"
	p.decorate(t, func(w http.ResponseWriter, r *http.Request, next http.Handler) {
		if r.URL.Path == prefix+strconv.Itoa(handle.Number)+"/results/outcome" {
			r.URL.Path = prefix + strconv.Itoa(other.Number) + "/results/outcome"
		}
		next.ServeHTTP(w, r)
	})
	if _, err := p.client(t).Result(context.Background(), handle, "outcome", parseRunID); err == nil || !strings.Contains(err.Error(), "retained binding") {
		t.Fatalf("a substituted archive was accepted: %v", err)
	}
}

func TestResultRefusesAResultNamingAnotherRun(t *testing.T) {
	p := newResultPlatform(t)
	run, handle := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d}`})
	_, forged := p.succeeded(t, map[string]string{"result.json": fmt.Sprintf(`{"run_id":%d}`, run.ID)})
	if _, err := p.client(t).Result(context.Background(), handle, "outcome", parseRunID); err != nil {
		t.Fatalf("the genuine result was refused: %v", err)
	}
	if _, err := p.client(t).Result(context.Background(), forged, "outcome", parseRunID); err == nil || !strings.Contains(err.Error(), "different Run") {
		t.Fatalf("a result from another Run was accepted: %v", err)
	}
}

func TestResultRefusals(t *testing.T) {
	p := newResultPlatform(t)
	_, published := p.succeeded(t, map[string]string{"result.json": `{"run_id":%d}`})
	t.Run("running", func(t *testing.T) {
		run := p.Admit(t, p.team, resultTemplate)
		handle := Handle{Team: p.team, Template: resultTemplate, Number: run.Number}
		if _, err := p.client(t).Result(context.Background(), handle, "outcome", parseRunID); err == nil || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("a running Run returned a result: %v", err)
		}
	})
	t.Run("failed", func(t *testing.T) {
		run := p.Admit(t, p.team, resultTemplate)
		p.Fail(t, p.team, resultTemplate, run.Number)
		handle := Handle{Team: p.team, Template: resultTemplate, Number: run.Number}
		if _, err := p.client(t).Result(context.Background(), handle, "outcome", parseRunID); err == nil || !strings.Contains(err.Error(), "no successful terminal result") {
			t.Fatalf("a failed Run returned a result: %v", err)
		}
	})
	t.Run("unknown result", func(t *testing.T) {
		if _, err := p.client(t).Result(context.Background(), published, "missing", parseRunID); err == nil {
			t.Fatal("an unbound result name returned a result")
		}
	})
	t.Run("parser error", func(t *testing.T) {
		refused := errors.New("refused")
		if _, err := p.client(t).Result(context.Background(), published, "outcome", func(fs.FS) (RunIdentified, error) { return nil, refused }); !errors.Is(err, refused) {
			t.Fatalf("parser refusal was not returned: %v", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, handle := p.succeeded(t, map[string]string{"other.json": `{}`})
		if _, err := p.client(t).Result(context.Background(), handle, "outcome", parseRunID); err == nil {
			t.Fatal("a result without its file was accepted")
		}
	})
	t.Run("no parser", func(t *testing.T) {
		if _, err := p.client(t).Result(context.Background(), published, "outcome", nil); err == nil {
			t.Fatal("a result was returned without a parser")
		}
	})
}

func TestReadResultFileRefusesNonRegularAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.json"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("result.json", filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "dir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	tree := os.DirFS(dir)
	if body, err := ReadResultFile(tree, "result.json", 10); err != nil || string(body) != "0123456789" {
		t.Fatalf("bounded regular file was refused: %q %v", body, err)
	}
	for _, name := range []string{"link.json", "dir.json", "absent.json"} {
		if _, err := ReadResultFile(tree, name, 10); err == nil {
			t.Fatalf("%s was read", name)
		}
	}
	if _, err := ReadResultFile(tree, "result.json", 9); err == nil {
		t.Fatal("an oversized file was read")
	}
}
