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
	HandoffID         output.HandoffID
}

type RunExecutionAdmission struct {
	RunExecutionRequest
	RunID    int
	Identity executioncontrol.Identity
}
