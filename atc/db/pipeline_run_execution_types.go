package db

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RunExecutionRequest binds a build step to the node's existing execution
// protocol. It contains no credential and is not another execution state store.
type RunExecutionRequest struct {
	BuildID           int
	PlanID            atc.PlanID
	Kind              ContainerType
	Epoch             int64
	NodeName, NodeUID string
	// Capture is the capture this execution produces, for a task with a Run
	// result; zero for every other execution.
	Capture output.CaptureKey
}

type RunExecutionAdmission struct {
	RunExecutionRequest
	RunID    int
	Identity executioncontrol.Identity
}

// RunOutputCancellationEvidence is supplied only by the trusted cleanup worker
// after contacting the exact node outside its transaction. It is not API input.
// An actual finish/stop must carry the node's verified signature. Never-started
// closure instead retains the node's accepted durable stop fence and a subsequent
// never-started classification; it must never fabricate a process outcome.
type RunOutputCancellationEvidence struct {
	NodeUID      string                                              `json:"node_uid"`
	Execution    executioncontrol.ClassifyResult                     `json:"execution"`
	StartClosure *executioncontrol.RequestSourcePreservingStopResult `json:"start_closure,omitempty"`
}
