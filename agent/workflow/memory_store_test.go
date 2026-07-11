package workflow_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func defYAML(name, promptBody string) []byte {
	return []byte(`schema_version: 1
name: ` + name + `
prompts:
  work: |
    ` + promptBody + `
steps:
- agent: work
  prompt: work
  outputs: [workspace]
`)
}

func TestMemoryStoreImportAssignsMonotonicVersions(t *testing.T) {
	s := workflow.NewMemoryStore()

	v1, err := s.Import("wf", defYAML("wf", "Do the work."), "alice")
	if err != nil {
		t.Fatalf("import v1: %v", err)
	}
	if v1.Version != 1 || v1.Name != "wf" || v1.CreatedBy != "alice" {
		t.Errorf("v1 = %+v", v1)
	}
	if v1.ContentHash != workflow.Hash(defYAML("wf", "Do the work.")) {
		t.Errorf("hash mismatch: %s", v1.ContentHash)
	}

	v2, err := s.Import("wf", defYAML("wf", "Do the work carefully."), "bob")
	if err != nil {
		t.Fatalf("import v2: %v", err)
	}
	if v2.Version != 2 {
		t.Errorf("v2.Version = %d, want 2", v2.Version)
	}
}

func TestMemoryStoreImportIsIdempotentOnHash(t *testing.T) {
	s := workflow.NewMemoryStore()
	raw := defYAML("wf", "Do the work.")

	v1, _ := s.Import("wf", raw, "alice")
	again, err := s.Import("wf", raw, "bob")
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if again.Version != v1.Version || again.CreatedBy != "alice" {
		t.Errorf("re-import must return the existing version untouched, got %+v", again)
	}
	versions, _ := s.Versions("wf")
	if len(versions) != 1 {
		t.Errorf("expected 1 stored version, got %d", len(versions))
	}
}

func TestMemoryStoreImportRejectsNameMismatchAndInvalid(t *testing.T) {
	s := workflow.NewMemoryStore()

	_, err := s.Import("other-name", defYAML("wf", "Do the work."), "alice")
	var inv workflow.InvalidDefinitionError
	if !errors.As(err, &inv) || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("name mismatch must be InvalidDefinitionError, got %v", err)
	}

	_, err = s.Import("wf", []byte("schema_version: 1\nname: wf\nsteps: []\n"), "alice")
	if !errors.As(err, &inv) {
		t.Errorf("validation failure must be InvalidDefinitionError, got %v", err)
	}
}

func TestMemoryStoreGetLiveAndPromote(t *testing.T) {
	s := workflow.NewMemoryStore()
	s.Import("wf", defYAML("wf", "One."), "alice")
	s.Import("wf", defYAML("wf", "Two."), "alice")

	if _, found, _ := s.Live("wf"); found {
		t.Error("nothing should be live before Promote")
	}

	if err := s.Promote("wf", 1, "alice"); err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	live, found, _ := s.Live("wf")
	if !found || live.Version != 1 {
		t.Fatalf("live = %+v, found=%v", live, found)
	}
	if live.RawYAML != string(defYAML("wf", "One.")) {
		t.Error("Live must populate RawYAML")
	}

	// Promotion atomically swaps: v2 live, v1 not.
	if err := s.Promote("wf", 2, "bob"); err != nil {
		t.Fatalf("promote v2: %v", err)
	}
	live, _, _ = s.Live("wf")
	if live.Version != 2 {
		t.Errorf("live.Version = %d, want 2", live.Version)
	}
	v1, _, _ := s.Get("wf", 1)
	if v1.Live {
		t.Error("v1 must no longer be live")
	}

	if err := s.Promote("wf", 99, "alice"); !errors.Is(err, workflow.ErrVersionNotFound) {
		t.Errorf("unknown version must be ErrVersionNotFound, got %v", err)
	}

	if _, found, _ := s.Get("wf", 99); found {
		t.Error("Get unknown version must report found=false")
	}
}

func TestMemoryStoreListReturnsLatestPerName(t *testing.T) {
	s := workflow.NewMemoryStore()
	s.Import("aa", defYAML("aa", "A one."), "alice")
	s.Import("aa", defYAML("aa", "A two."), "alice")
	s.Import("bb", defYAML("bb", "B one."), "alice")

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List len = %d, want 2", len(list))
	}
	if list[0].Name != "aa" || list[0].Version != 2 {
		t.Errorf("list[0] = %+v", list[0])
	}
	if list[1].Name != "bb" || list[1].Version != 1 {
		t.Errorf("list[1] = %+v", list[1])
	}
}
