package workflow_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestManifestValidateAcceptsCleanTree(t *testing.T) {
	m := workflow.Manifest{
		"workflow.yml":        "name: x\n",
		"prompts/work.md":     "do it",
		"skills/tdd/SKILL.md": "# tdd",
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestManifestValidateRejectsBadPaths(t *testing.T) {
	cases := map[string]workflow.Manifest{
		"empty manifest": {},
		"absolute":       {"/etc/passwd": "x"},
		"dotdot":         {"a/../b": "x"},
		"dot segment":    {"a/./b": "x"},
		"empty segment":  {"a//b": "x"},
		"hidden segment": {".claude/settings.json": "x"},
		"backslash":      {`a\b`: "x"},
		"empty path":     {"": "x"},
		"not utf8":       {"bin": string([]byte{0xff, 0xfe})},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestManifestValidateEnforcesCaps(t *testing.T) {
	big := workflow.Manifest{"big.md": strings.Repeat("a", workflow.MaxManifestFileBytes+1)}
	if err := big.Validate(); err == nil {
		t.Error("per-file cap: expected error")
	}

	many := workflow.Manifest{}
	for i := 0; i <= workflow.MaxManifestFiles; i++ {
		many[fmt.Sprintf("f%04d.md", i)] = "x"
	}
	if err := many.Validate(); err == nil {
		t.Error("file-count cap: expected error")
	}
}

func TestManifestCanonicalIsDeterministicAndPinned(t *testing.T) {
	a := workflow.Manifest{"b.md": "2", "a.md": "1"}
	b := workflow.Manifest{"a.md": "1", "b.md": "2"}
	want := `{"a.md":"1","b.md":"2"}`
	if got := string(a.Canonical()); got != want {
		t.Fatalf("canonical form drifted: got %s want %s", got, want)
	}
	if a.Hash() != b.Hash() {
		t.Fatal("insertion order changed the hash")
	}
	if len(a.Hash()) != 64 {
		t.Fatal("hash is not sha256-hex")
	}
}

func TestManifestPathsSorted(t *testing.T) {
	m := workflow.Manifest{"b": "", "a": "", "c": ""}
	got := m.Paths()
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestManifestV3EveryAssetIsHashed(t *testing.T) {
	base := workflow.Manifest{
		"workflow.yml":                 "schema_version: 3\nname: hash-test\nsignature_version: 1\ninputs: []\noutputs: []\nplan: [{agent: work, prompt_file: prompts/work.md}]\n",
		"prompts/work.md":              "prompt",
		"prompts/system.md":            "system",
		"context/conventions.md":       "context",
		"skills/testing/SKILL.md":      "skill",
		"skills/testing/refs/rules.md": "rules",
	}
	for _, path := range []string{
		"workflow.yml",
		"prompts/work.md",
		"prompts/system.md",
		"context/conventions.md",
		"skills/testing/SKILL.md",
		"skills/testing/refs/rules.md",
	} {
		t.Run(path, func(t *testing.T) {
			changed := cloneManifest(base)
			changed[path] += " changed"
			if changed.Hash() == base.Hash() {
				t.Fatalf("changing %q did not change the source hash", path)
			}
		})
	}
}

func TestManifestV3UnreferencedFilesRemainHashed(t *testing.T) {
	base := workflow.Manifest{
		"workflow.yml":    "schema_version: 3\nname: hash-test\nsignature_version: 1\ninputs: []\noutputs: []\nplan: [{agent: work, prompt: work}]\n",
		"README.md":       "one",
		"notes/design.md": "unchanged",
	}
	changed := cloneManifest(base)
	changed["README.md"] = "two"
	if changed.Hash() == base.Hash() {
		t.Fatal("an unreferenced source edit did not mint a distinct content hash")
	}

	baseCompiled, err := workflow.CompileDefinition(base)
	if err != nil {
		t.Fatalf("compile base: %v", err)
	}
	changedCompiled, err := workflow.CompileDefinition(changed)
	if err != nil {
		t.Fatalf("compile changed: %v", err)
	}
	if !reflect.DeepEqual(baseCompiled, changedCompiled) {
		t.Fatalf("unreferenced content changed executable compilation:\nbase=%+v\nchanged=%+v", baseCompiled, changedCompiled)
	}
}

func cloneManifest(source workflow.Manifest) workflow.Manifest {
	copy := make(workflow.Manifest, len(source))
	for path, content := range source {
		copy[path] = content
	}
	return copy
}
