// Package provenance is the workflow-run-facing façade for the neutral ATC
// plan provenance implementation. Persistence packages should import the
// neutral atc/workflowprovenance package directly to avoid dependency cycles.
package provenance

import (
	"encoding/json"

	"github.com/concourse/concourse/atc"
	neutral "github.com/concourse/concourse/atc/workflowprovenance"
)

type Input = neutral.Input
type Capture = neutral.Capture
type ExpectedOutput = neutral.ExpectedOutput
type ExpectedProducer = neutral.ExpectedProducer
type DependencyEnvelope = neutral.DependencyEnvelope
type ResourceDependency = neutral.ResourceDependency
type ImageKind = neutral.ImageKind
type ImageDependency = neutral.ImageDependency

const (
	MaxCanonicalPlanBytes  = neutral.MaxCanonicalPlanBytes
	MaxDependencyJSONBytes = neutral.MaxDependencyJSONBytes
	MaxDependencyRecords   = neutral.MaxDependencyRecords
	MaxWorkflowOutputs     = neutral.MaxWorkflowOutputs

	ImageKindTask               = neutral.ImageKindTask
	ImageKindCustomResourceType = neutral.ImageKindCustomResourceType
	ImageKindCapabilitySidecar  = neutral.ImageKindCapabilitySidecar
)

var (
	ErrInvalidProvenance      = neutral.ErrInvalidProvenance
	ErrOutputContractMismatch = neutral.ErrOutputContractMismatch
	ErrDynamicWorkflowOutput  = neutral.ErrDynamicWorkflowOutput
	ErrLimitExceeded          = neutral.ErrLimitExceeded
)

func FromPlan(input Input, plan atc.Plan) (Capture, error) {
	return neutral.FromPlan(input, plan)
}

func VerifyFrozen(
	input Input,
	actualPlan json.RawMessage,
	actualPlanHash string,
	resolvedDependencies json.RawMessage,
) (Capture, error) {
	return neutral.VerifyFrozen(input, actualPlan, actualPlanHash, resolvedDependencies)
}
