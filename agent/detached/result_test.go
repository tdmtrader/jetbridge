package detached

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/atc"
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

func succeededPlatform(t *testing.T, files map[string]string) *fakePlatform {
	platform := newFakePlatform(t, "source", "outcome")
	archive, binding := canonicalResult(t, files)
	platform.archive = archive
	platform.run.Status = atc.RunStatusSucceeded
	platform.run.Terminal = &atc.RunTerminalResult{Status: atc.RunStatusSucceeded, Results: map[string]atc.RunResultBinding{"outcome": binding}}
	return platform
}

func handle() Handle { return Handle{Team: testTeam, Template: testTemplate, Number: testNumber} }

func TestResultVerifiesTheBindingThenTheRun(t *testing.T) {
	platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
	parsed, err := platform.client(t).Result(context.Background(), handle(), "outcome", parseRunID)
	if err != nil || parsed.RunID() != testRunID {
		t.Fatalf("verified result was refused: %v %v", parsed, err)
	}
}

func TestResultRefusesADigestMismatchBeforeParsing(t *testing.T) {
	platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
	binding := platform.run.Terminal.Results["outcome"]
	binding.Ref.Digest = hangar.Digest("sha256:" + strings.Repeat("0", 64))
	platform.run.Terminal.Results["outcome"] = binding
	called := false
	_, err := platform.client(t).Result(context.Background(), handle(), "outcome", func(tree fs.FS) (RunIdentified, error) {
		called = true
		return parseRunID(tree)
	})
	if err == nil || !strings.Contains(err.Error(), "retained binding") || called {
		t.Fatalf("a mismatched archive reached the parser: called=%v err=%v", called, err)
	}
}

func TestResultRefusesAnArchiveFromAnotherResult(t *testing.T) {
	platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
	other, _ := canonicalResult(t, map[string]string{"result.json": `{"run_id":41, "extra":1}`})
	platform.archive = other
	if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", parseRunID); err == nil {
		t.Fatal("a substituted archive was accepted")
	}
}

func TestResultRefusesAResultNamingAnotherRun(t *testing.T) {
	platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":40}`})
	if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", parseRunID); err == nil || !strings.Contains(err.Error(), "different Run") {
		t.Fatalf("a result from another Run was accepted: %v", err)
	}
}

func TestResultRefusals(t *testing.T) {
	t.Run("running", func(t *testing.T) {
		platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
		platform.run.Status = atc.RunStatusRunning
		if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", parseRunID); err == nil {
			t.Fatal("a running Run returned a result")
		}
	})
	t.Run("failed", func(t *testing.T) {
		platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
		platform.run.Status = atc.RunStatusFailed
		platform.run.Terminal = &atc.RunTerminalResult{Status: atc.RunStatusFailed, Results: map[string]atc.RunResultBinding{}}
		if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", parseRunID); err == nil {
			t.Fatal("a failed Run returned a result")
		}
	})
	t.Run("unknown result", func(t *testing.T) {
		platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
		if _, err := platform.client(t).Result(context.Background(), handle(), "missing", parseRunID); err == nil {
			t.Fatal("an unbound result name returned a result")
		}
	})
	t.Run("parser error", func(t *testing.T) {
		platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
		refused := errors.New("refused")
		if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", func(fs.FS) (RunIdentified, error) { return nil, refused }); !errors.Is(err, refused) {
			t.Fatalf("parser refusal was not returned: %v", err)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		platform := succeededPlatform(t, map[string]string{"other.json": `{}`})
		if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", parseRunID); err == nil {
			t.Fatal("a result without its file was accepted")
		}
	})
	t.Run("no parser", func(t *testing.T) {
		platform := succeededPlatform(t, map[string]string{"result.json": `{"run_id":41}`})
		if _, err := platform.client(t).Result(context.Background(), handle(), "outcome", nil); err == nil {
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
