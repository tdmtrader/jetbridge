package workflow_test

import (
	"testing"

	"github.com/concourse/concourse/agent/workflow"
)

func TestHashMatchesPhaseconfigSemantics(t *testing.T) {
	// hex(sha256("hello")) — fixed vector so the fn provably matches
	// ci-agent/phaseconfig.Hash (same input → same output).
	got := workflow.Hash([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("Hash = %s, want %s", got, want)
	}
	if workflow.Hash([]byte("hello")) == workflow.Hash([]byte("hello\n")) {
		t.Error("Hash must be byte-sensitive")
	}
}
