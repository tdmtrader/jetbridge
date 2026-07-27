package workflow_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManifestFromDirWalksAndSkipsHidden(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		"workflow.yml":        "name: x\n",
		"prompts/work.md":     "body",
		"skills/tdd/SKILL.md": "# tdd",
		".DS_Store":           "junk",
		".git/config":         "junk",
	})
	m, err := workflow.ManifestFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"prompts/work.md", "skills/tdd/SKILL.md", "workflow.yml"}
	got := m.Paths()
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestManifestFromDirDereferencesSymlinks(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"lib/skills/tdd/SKILL.md": "# shared tdd",
		"develop/workflow.yml":    "name: develop\n",
	})
	if err := os.MkdirAll(filepath.Join(root, "develop/skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "lib/skills/tdd"), filepath.Join(root, "develop/skills/tdd")); err != nil {
		t.Fatal(err)
	}
	m, err := workflow.ManifestFromDir(filepath.Join(root, "develop"))
	if err != nil {
		t.Fatal(err)
	}
	if m["skills/tdd/SKILL.md"] != "# shared tdd" {
		t.Fatalf("symlinked skill not dereferenced: %v", m.Paths())
	}
}

func TestManifestFromDirDetectsSymlinkCycle(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{"workflow.yml": "name: x\n"})
	if err := os.Symlink(root, filepath.Join(root, "loop")); err != nil {
		t.Fatal(err)
	}
	if _, err := workflow.ManifestFromDir(root); err == nil {
		t.Fatal("expected symlink-cycle error")
	}
}

func TestDiscoverWorkflowDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"develop/workflow.yml":  "name: develop\n",
		"analyze/workflow.yml":  "name: analyze\n",
		"lib/skills/x/SKILL.md": "# not a workflow",
	})
	dirs, err := workflow.DiscoverWorkflowDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 || filepath.Base(dirs[0]) != "analyze" || filepath.Base(dirs[1]) != "develop" {
		t.Fatalf("got %v", dirs)
	}

	single, err := workflow.DiscoverWorkflowDirs(filepath.Join(root, "develop"))
	if err != nil || len(single) != 1 {
		t.Fatalf("single-dir root: got %v, %v", single, err)
	}

	_, err = workflow.DiscoverWorkflowDirs(filepath.Join(root, "lib"))
	if err == nil {
		t.Fatal("expected no-workflow-file error")
	}
	wantErr := "no workflow.yaml (or legacy workflow.yml) in the directory or its immediate subdirectories"
	if !strings.Contains(err.Error(), wantErr) {
		t.Fatalf("error = %q, want substring %q", err, wantErr)
	}
}

func TestDiscoverWorkflowDirsPrefersWorkflowYAML(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"workflow.yaml": "name: preferred\n",
	})
	dirs, err := workflow.DiscoverWorkflowDirs(root)
	if err != nil || len(dirs) != 1 || dirs[0] != root {
		t.Fatalf("workflow.yaml root: got %v, %v", dirs, err)
	}
}

func TestDiscoverWorkflowDirsFallsBackToLegacyYML(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"legacy/workflow.yml": "name: legacy\n",
	})
	dirs, err := workflow.DiscoverWorkflowDirs(root)
	if err != nil || len(dirs) != 1 || filepath.Base(dirs[0]) != "legacy" {
		t.Fatalf("legacy fallback: got %v, %v", dirs, err)
	}
}

func TestDiscoverWorkflowDirsPrefersYAMLOverLegacyWhenBothPresent(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"workflow.yaml": "name: preferred\n",
		"workflow.yml":  "name: legacy\n",
	})
	dirs, err := workflow.DiscoverWorkflowDirs(root)
	if err != nil || len(dirs) != 1 || dirs[0] != root {
		t.Fatalf("both-present root: got %v, %v", dirs, err)
	}
}
