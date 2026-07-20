package harvest_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

// gitFixture builds a two-commit repo: baseFiles are committed first
// (returned baseSHA points at that commit), then headFiles are written and
// committed on top so ChangedPaths(dir, baseSHA) == the headFiles set.
func gitFixture(t *testing.T, baseFiles, headFiles map[string]string) (dir, baseSHA string) {
	t.Helper()
	dir = t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeAll := func(files map[string]string) {
		for name, content := range files {
			full := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	run("init", "-q")
	writeAll(baseFiles)
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	baseSHA = string(out[:len(out)-1]) // strip trailing newline
	writeAll(headFiles)
	run("add", "-A")
	run("commit", "-q", "-m", "head")
	return dir, baseSHA
}

func TestElmGateNotApplicableWhenElmUntouched(t *testing.T) {
	dir, base := gitFixture(t,
		map[string]string{"README.md": "hi\n"},
		map[string]string{"README.md": "hi there\n"}, // no web/elm change
	)
	policy := harvest.GatePolicy{Gates: []harvest.Gate{{Gate: "elm-build", Scope: "full"}}}

	outcomes, err := harvest.RunGates(policy, dir, base, nil)
	if err != nil {
		t.Fatalf("RunGates: %v", err)
	}
	if len(outcomes) != 1 || outcomes[0].Status != "ok" {
		t.Fatalf("outcomes = %+v, want a single ok gate (elm untouched => not applicable)", outcomes)
	}
	if !contains(outcomes[0].Detail, "not applicable") {
		t.Errorf("detail = %q, want it to say the gate was not applicable", outcomes[0].Detail)
	}
}
