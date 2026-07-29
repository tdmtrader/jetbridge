package provider

// approvedClaudeRecoveryProofs is intentionally empty. The agent-runner
// image's npm pin is not evidence of the executable display line, session
// format, credential boundary, or native resume behavior. Add an entry only
// after an exact executable/session harness has been reviewed.
var approvedClaudeRecoveryProofs = map[string]RecoveryProof{}

// LookupClaudeRecoveryProof accepts only a byte-for-byte approved Claude
// executable probe output. Unknown versions and any output drift fail closed.
func LookupClaudeRecoveryProof(executableOutput string) (RecoveryProof, bool) {
	proof, found := approvedClaudeRecoveryProofs[executableOutput]
	return proof, found
}
