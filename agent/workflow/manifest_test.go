package workflow_test

import (
	"fmt"
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
