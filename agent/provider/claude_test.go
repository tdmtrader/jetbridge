package provider

import "testing"

func TestClaudeRecoveryProofFailsClosedWithoutAnApprovedExactExecutableOutput(t *testing.T) {
	for _, output := range []string{
		"2.0.1",
		"claude 2.0.1",
		"claude 2.0.1\n",
		"claude 2.0.1 (unexpected build)",
	} {
		if _, found := LookupClaudeRecoveryProof(output); found {
			t.Fatalf("LookupClaudeRecoveryProof(%q) unexpectedly approved native resume", output)
		}
	}
}
