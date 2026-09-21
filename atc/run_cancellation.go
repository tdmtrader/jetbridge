package atc

import "time"

// RunCancelOutcome describes acceptance of a durable command, not a new Run status.
type RunCancelOutcome string

const (
	RunCancelAccepted         RunCancelOutcome = "accepted"
	RunCancelAlreadyRequested RunCancelOutcome = "already_requested"
	RunCancelAlreadyTerminal  RunCancelOutcome = "already_terminal"
)

// RunCancellationRequest is only presented to an authorized viewer of its Run.
type RunCancellationRequest struct {
	RequestedAt time.Time `json:"requested_at"`
	RequestedBy string    `json:"requested_by"`
	Reason      *string   `json:"reason,omitempty"`
}

type CancelPipelineRunResponse struct {
	Outcome RunCancelOutcome `json:"outcome"`
	Run     PipelineRun      `json:"run"`
}
