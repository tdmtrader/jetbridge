package steps

import (
	"os"
	"path/filepath"
	"testing"
)

// Retained from the retired host-executor tests: disposal must use private
// allocation ownership, not the public path carried in a scenario state.
func TestTaskWorkspaceDisposalIsIsolated(t *testing.T) {
	resource := TaskWorkspaceResourceDefinition()
	newWorkspace := func() TaskWorkspace {
		t.Helper()
		value, err := resource.Factory(nil)
		if err != nil {
			t.Fatal(err)
		}
		w := value.(TaskWorkspace)
		t.Cleanup(func() { _ = os.RemoveAll(w.ownedDir) })
		return w
	}
	first, second := newWorkspace(), newWorkspace()
	outside := t.TempDir()
	for _, dir := range []string{first.Dir, second.Dir, outside} {
		if err := os.WriteFile(filepath.Join(dir, "sentinel"), []byte(dir), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := resource.Disposer(TaskWorkspace{Dir: outside}); err == nil {
		t.Fatal("disposer accepted an unowned workspace")
	}
	owned := first.Dir
	first.Dir = outside
	if err := resource.Disposer(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("owned workspace survived disposal: %v", err)
	}
	for _, dir := range []string{second.Dir, outside} {
		if data, err := os.ReadFile(filepath.Join(dir, "sentinel")); err != nil || string(data) != dir {
			t.Fatalf("disposing one workspace damaged another: %q, %v", data, err)
		}
	}
	if err := resource.Disposer(second); err != nil {
		t.Fatal(err)
	}
}
