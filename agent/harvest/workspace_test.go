package harvest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/harvest"
)

// workspaceWithHistory: workspaceWithRemote plus a second commit, so the
// manifest has two entries and base..HEAD is non-trivial.
func workspaceWithHistory(t *testing.T) (workspace, remote string) {
	t.Helper()
	workspace, remote = workspaceWithRemote(t)
	os.WriteFile(filepath.Join(workspace, "more.md"), []byte("more work\n"), 0644)
	git(t, workspace, "add", ".")
	git(t, workspace, "commit", "-m", "second agent commit")
	return workspace, remote
}

func TestHeadAndBaseSHA(t *testing.T) {
	ws, _ := workspaceWithHistory(t)

	head, err := harvest.HeadSHA(ws)
	if err != nil || len(head) != 40 {
		t.Fatalf("HeadSHA: %q, %v", head, err)
	}
	base, err := harvest.BaseSHA(ws, "main")
	if err != nil {
		t.Fatalf("BaseSHA: %v", err)
	}
	if base == head {
		t.Fatal("base must differ from head (two commits on top of main)")
	}
	if got := git(t, ws, "rev-parse", "origin/main"); got != base {
		t.Fatalf("base %s != origin/main %s", base, got)
	}
}

func TestBaseSHADefaultsToMain(t *testing.T) {
	ws, _ := workspaceWithHistory(t)
	a, err1 := harvest.BaseSHA(ws, "")
	b, err2 := harvest.BaseSHA(ws, "main")
	if err1 != nil || err2 != nil || a != b {
		t.Fatalf("empty target must default to main: %q/%v vs %q/%v", a, err1, b, err2)
	}
}

func TestChangedPathsAndDiff(t *testing.T) {
	ws, _ := workspaceWithHistory(t)
	base, _ := harvest.BaseSHA(ws, "main")

	paths, err := harvest.ChangedPaths(ws, base)
	if err != nil {
		t.Fatalf("ChangedPaths: %v", err)
	}
	want := map[string]bool{"report.md": true, "more.md": true}
	if len(paths) != 2 || !want[paths[0]] || !want[paths[1]] {
		t.Fatalf("ChangedPaths = %v", paths)
	}

	diff, err := harvest.Diff(ws, base, 1<<20)
	if err != nil || !strings.Contains(diff, "report.md") {
		t.Fatalf("Diff: %v\n%s", err, diff)
	}

	tiny, err := harvest.Diff(ws, base, 10)
	if err != nil {
		t.Fatalf("Diff truncated: %v", err)
	}
	if !strings.HasSuffix(tiny, harvest.DiffTruncatedMarker) {
		t.Fatalf("truncated diff must end with the marker: %q", tiny)
	}
	if len(tiny) > 10+len(harvest.DiffTruncatedMarker) {
		t.Fatalf("truncated diff too long: %d", len(tiny))
	}
}

func TestBuildManifest(t *testing.T) {
	ws, _ := workspaceWithHistory(t)
	head, _ := harvest.HeadSHA(ws)
	base, _ := harvest.BaseSHA(ws, "main")

	m, err := harvest.BuildManifest(ws, base, head, "tdmtrader/concourse", "agent/ticket-42")
	if err != nil {
		t.Fatalf("BuildManifest: %v", err)
	}
	if m.Repo != "tdmtrader/concourse" || m.Branch != "agent/ticket-42" ||
		m.BaseSHA != base || m.HeadSHA != head {
		t.Fatalf("manifest header wrong: %+v", m)
	}
	if len(m.Commits) != 2 {
		t.Fatalf("want 2 commits oldest-first, got %+v", m.Commits)
	}
	if m.Commits[0].Subject != "agent work for ticket 42" {
		t.Fatalf("oldest-first violated: %+v", m.Commits)
	}
	if len(m.Files) != 2 {
		t.Fatalf("want 2 files, got %+v", m.Files)
	}
	for _, f := range m.Files {
		if f.Added < 1 {
			t.Fatalf("numstat not parsed: %+v", f)
		}
	}
}
