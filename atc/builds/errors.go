package builds

import (
	"fmt"
)

// UnknownResourceError is returned when a 'get' or 'put' step refers to a
// resource which is not in the set of resources provided to the Planner.
type UnknownResourceError struct {
	Resource string
}

func (err UnknownResourceError) Error() string {
	return fmt.Sprintf("unknown resource: %s", err.Resource)
}

// VersionNotProvidedError is returned when a 'get' step does not have a
// corresponding input provided to the Planner.
type VersionNotProvidedError struct {
	Input string
}

func (err VersionNotProvidedError) Error() string {
	return fmt.Sprintf("version for input %s not provided", err.Input)
}

// RunPipelineInputError is returned when an input of a 'run_pipeline' step
// names, in its 'from', a step it cannot bind: one that is not an earlier
// waited 'run_pipeline' step of the same job.
type RunPipelineInputError struct {
	Step   string
	Input  string
	From   string
	Reason string
}

func (err RunPipelineInputError) Error() string {
	return fmt.Sprintf("run_pipeline(%s) input %s: from %s %s", err.Step, err.Input, err.From, err.Reason)
}

// The reasons a RunPipelineInputError gives.
const (
	RunPipelineFromUnknown   = "names no earlier run_pipeline step of this job"
	RunPipelineFromNotWaited = "is a run_pipeline step without wait: true"
	RunPipelineFromParallel  = "is called by more than one step of an in_parallel"
	RunPipelineFromRetry     = "is inside a retry's attempts, and which attempt succeeds is not known when the build is planned"
	RunPipelineFromAcross    = "is inside an earlier across step, whose iterations each start their own run"
)
