package exec

import (
	"context"
	"io"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"go.opentelemetry.io/otel/trace"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/tracing"
)

type BuildStepDelegateFactory interface {
	BuildStepDelegate(state RunState) BuildStepDelegate
}

type BuildStepDelegate interface {
	StartSpan(context.Context, string, tracing.Attrs) (context.Context, trace.Span)

	FetchImage(context.Context, atc.Plan, *atc.Plan, bool) (runtime.ImageSpec, db.ResourceCache, error)

	Stdout() io.Writer
	Stderr() io.Writer

	Initializing(lager.Logger)
	Starting(lager.Logger)
	Finished(lager.Logger, bool)
	Errored(lager.Logger, string)

	BeforeSelectWorker(lager.Logger) error
	WaitingForWorker(lager.Logger)
	SelectedWorker(lager.Logger, string)
	BuildStartTime() time.Time

	ConstructAcrossSubsteps([]byte, []atc.AcrossVar, [][]any) ([]atc.VarScopedPlan, error)
	ContainerOwner(planId atc.PlanID) db.ContainerOwner
}

type SetPipelineStepDelegateFactory interface {
	SetPipelineStepDelegate(state RunState) SetPipelineStepDelegate
}

type SetPipelineStepDelegate interface {
	BuildStepDelegate
	SetPipelineChanged(lager.Logger, bool)
	CheckRunSetPipelinePolicy(*atc.Config) error
}

type RunPipelineStepDelegateFactory interface {
	RunPipelineStepDelegate(state RunState) RunPipelineStepDelegate
}

// RunPipelineStepDelegate is a BuildStepDelegate plus the one thing the step
// cannot do for itself. The step reports what it did on stdout and stderr and
// emits no event of its own -- there is no child status to observe in slice 1,
// so there is nothing for a wider delegate to carry -- but it does have to be
// screened by the policy checker, which lives on the build's side of this
// interface.
type RunPipelineStepDelegate interface {
	BuildStepDelegate

	// CheckRunPipelinePolicy screens one admission before it happens.
	//
	// Creating a run over HTTP goes through the API's policy wrappa. A build
	// creating one reaches no route, so a policy agent that blocks run
	// creation would be inert for build-initiated runs unless the step asks --
	// the gap set_pipeline closes the same way with
	// SetPipelineStepDelegate.CheckRunSetPipelinePolicy.
	//
	// team is the team the run would be created on and pipeline is the
	// template it names; the calling build's own team and pipeline are the
	// delegate's to supply. params are the interpolated ones, because those
	// are what would actually run.
	CheckRunPipelinePolicy(team string, pipeline string, params atc.RunParams) error
}

// RunPipelinePolicyData is what the policy agent is shown about an admission,
// as the check input's data.
//
// It is declared here, next to the method that produces it, so the shape a
// policy is written against is stated once rather than assembled inline at the
// delegate. set_pipeline hands over the whole *atc.Config it is about to save;
// the equivalent for a run is what identifies it -- the template it names, the
// team it lands on, and the params it carries.
type RunPipelinePolicyData struct {
	Team     string        `json:"team"`
	Pipeline string        `json:"pipeline"`
	Params   atc.RunParams `json:"params"`
}
