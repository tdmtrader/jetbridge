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
	if v1.ContentHash != (workflow.Manifest{"workflow.yml": string(defYAML("wf", "Do the work."))}).Hash() {
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

func TestMemoryStoreLiveVersions(t *testing.T) {
	s := workflow.NewMemoryStore()

	// wf-a: v1 promoted, then v2 imported (live stays at 1).
	if _, err := s.Import("wf-a", defYAML("wf-a", "One."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if err := s.Promote("wf-a", 1, "alice"); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := s.Import("wf-a", defYAML("wf-a", "Two."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}
	// wf-b: never promoted.
	if _, err := s.Import("wf-b", defYAML("wf-b", "B."), "bob"); err != nil {
		t.Fatalf("import: %v", err)
	}

	live, err := s.LiveVersions()
	if err != nil {
		t.Fatalf("live versions: %v", err)
	}
	if got, want := live["wf-a"], 1; got != want {
		t.Errorf("wf-a live = %d, want %d", got, want)
	}
	if _, ok := live["wf-b"]; ok {
		t.Errorf("wf-b should have no live version, got %d", live["wf-b"])
	}
}

func TestMemoryStoreLatest(t *testing.T) {
	s := workflow.NewMemoryStore()

	if _, err := s.Import("wf", defYAML("wf", "One."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}
	if _, err := s.Import("wf", defYAML("wf", "Two."), "alice"); err != nil {
		t.Fatalf("import: %v", err)
	}

	def, found, err := s.Latest("wf")
	if err != nil || !found {
		t.Fatalf("latest: found=%v err=%v", found, err)
	}
	if def.Version != 2 {
		t.Errorf("latest version = %d, want 2", def.Version)
	}

	_, found, err = s.Latest("nope")
	if err != nil {
		t.Fatalf("latest unknown: %v", err)
	}
	if found {
		t.Error("unknown workflow reported found")
	}
}

func TestMemoryStoreImportManifest(t *testing.T) {
	store := workflow.NewMemoryStore()

	m := v2Manifest() // from compile_test.go
	def, err := store.ImportManifest("dev", m, "alice")
	if err != nil {
		t.Fatalf("import manifest: %v", err)
	}
	if def.ContentHash != m.Hash() {
		t.Fatalf("hash must be the canonical-manifest hash: %s vs %s", def.ContentHash, m.Hash())
	}
	if def.RawYAML != m["workflow.yml"] {
		t.Fatal("RawYAML must be the manifest's workflow.yml")
	}
	if def.Config.SkillFiles["skills/tdd/SKILL.md"] == "" {
		t.Fatal("stored Config must be compiled (skill trees resolved)")
	}

	again, err := store.ImportManifest("dev", m, "bob")
	if err != nil || again.Version != def.Version {
		t.Fatalf("expected idempotent hit, got v%d err %v", again.Version, err)
	}

	got, found, err := store.Get("dev", def.Version)
	if err != nil || !found {
		t.Fatalf("get: %v %v", found, err)
	}
	if got.SourceManifest["prompts/implement.md"] == "" {
		t.Fatal("Get must return the source manifest")
	}

	// Import(raw) is the single-file degenerate case: same hash scheme.
	raw := []byte(validV1YAML())
	viaRaw, err := store.Import("v1", raw, "alice")
	if err != nil {
		t.Fatal(err)
	}
	wantHash := workflow.Manifest{"workflow.yml": string(raw)}.Hash()
	if viaRaw.ContentHash != wantHash {
		t.Fatalf("raw import must wrap into a single-file manifest: %s vs %s", viaRaw.ContentHash, wantHash)
	}

	// Metadata listings stay lean.
	list, _ := store.List()
	for _, d := range list {
		if d.RawYAML != "" || len(d.SourceManifest) != 0 {
			t.Fatal("List must not carry RawYAML/SourceManifest")
		}
	}
}
