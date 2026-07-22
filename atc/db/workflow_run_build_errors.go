package db

import "fmt"

// AgentWorkflowRunProvenanceError identifies a semantic plan/provenance
// failure. The scheduler may terminalize the durable workflow for this class;
// transaction, encryption, and notification failures remain retryable.
type AgentWorkflowRunProvenanceError struct {
	Cause error
}

func (err *AgentWorkflowRunProvenanceError) Error() string {
	return fmt.Sprintf("db: workflow-run plan provenance: %v", err.Cause)
}

func (err *AgentWorkflowRunProvenanceError) Unwrap() error { return err.Cause }
