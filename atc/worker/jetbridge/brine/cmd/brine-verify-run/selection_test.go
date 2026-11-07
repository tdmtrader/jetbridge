package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// Coverage launches whole manifests separately. Prove with the real pinned
// CLI that their union is exactly the default discovered suite, including
// scenario outlines, budgets and file ownership. A newly discoverable tier
// must not silently disappear from the coverage job.
func TestCoverageTiersPartitionDiscoveredSuite(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	type manifest struct {
		Path     string
		Contract int
		Budgets  json.RawMessage
		Files    []string
	}
	type featureFile struct {
		Path          string
		ClaimedBy     []string `json:"claimed_by"`
		ScenarioCount int      `json:"scenario_count"`
		Features      json.RawMessage
	}
	type suite struct {
		SchemaVersion int `json:"schema_version"`
		Manifests     []manifest
		Files         []featureFile
	}
	read := func(path string) suite {
		t.Helper()
		cmd := exec.Command("brine", "suite", path, "--json")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("real Brine suite discovery at %s: %v", path, err)
		}
		var s suite
		if err := json.Unmarshal(out, &s); err != nil {
			t.Fatal(err)
		}
		if s.SchemaVersion != 1 || len(s.Manifests) == 0 || len(s.Files) == 0 {
			t.Fatalf("missing suite inventory at %s", path)
		}
		return s
	}
	want := read(root)
	wantManifests, gotManifests := map[string]manifest{}, map[string]manifest{}
	wantFiles, gotFiles := map[string]featureFile{}, map[string]featureFile{}
	for _, m := range want.Manifests {
		wantManifests[m.Path] = m
	}
	for _, f := range want.Files {
		wantFiles[f.Path] = f
	}
	for _, tier := range []string{"local", "live/main", "live/recovery"} {
		s := read(filepath.Join(root, tier))
		for _, m := range s.Manifests {
			if _, duplicate := gotManifests[m.Path]; duplicate {
				t.Fatalf("manifest runs in more than one tier: %s", m.Path)
			}
			gotManifests[m.Path] = m
		}
		for _, f := range s.Files {
			if _, duplicate := gotFiles[f.Path]; duplicate {
				t.Fatalf("feature file runs in more than one tier: %s", f.Path)
			}
			if f.ScenarioCount == 0 || len(f.ClaimedBy) != 1 || len(f.Features) == 0 {
				t.Fatalf("empty or ambiguously owned feature file: %s", f.Path)
			}
			gotFiles[f.Path] = f
		}
	}
	if !reflect.DeepEqual(gotManifests, wantManifests) {
		t.Error("coverage tier manifests/budgets differ from default suite discovery")
	}
	if !reflect.DeepEqual(gotFiles, wantFiles) {
		t.Error("coverage tier scenarios differ from default suite discovery")
	}
}
