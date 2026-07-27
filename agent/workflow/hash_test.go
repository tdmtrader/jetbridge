package workflow_test

import (
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestHashMatchesPhaseconfigSemantics(t *testing.T) {
	// hex(sha256("hello")) — fixed vector pinning the content-hash semantics
	// so the function can never silently change what it hashes.
	got := workflow.Hash([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("Hash = %s, want %s", got, want)
	}
	if workflow.Hash([]byte("hello")) == workflow.Hash([]byte("hello\n")) {
		t.Error("Hash must be byte-sensitive")
	}
}

func TestHashUnaffectedByCompiledModel(t *testing.T) {
	manifest := workflow.Manifest{"workflow.yml": "hello"}
	const wantManifestHash = "d751b547b9c1e2f93311395532e2ada8ff1d5e8b17cfaa2d9da6615b79f3c442"
	if got := manifest.Hash(); got != wantManifestHash {
		t.Fatalf("Manifest.Hash = %s, want %s", got, wantManifestHash)
	}

	raw := []byte(v3ProgramYAML)
	wantRawHash := workflow.Hash(raw)
	if _, err := workflow.ParseCompiled(raw); err != nil {
		t.Fatalf("ParseCompiled: %v", err)
	}
	if got := workflow.Hash(raw); got != wantRawHash {
		t.Fatalf("parsing changed raw-byte identity: got %s want %s", got, wantRawHash)
	}
}
