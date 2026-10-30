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

	FetchImage(context.Context, atc.Plan, bool) (runtime.ImageSpec, db.ResourceCache, error)
	PinnedImage(context.Context, string, bool) (runtime.ImageSpec, db.ResourceCache, error)

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
}

type RunPipelineStepDelegateFactory interface {
	RunPipelineStepDelegate(state RunState) RunPipelineStepDelegate
}

// RunPipelineStepDelegate is the delegate the run_pipeline step reports
// through. The step reports what it did on stdout and stderr and emits no
// event of its own, so there is nothing for a wider delegate to carry.
type RunPipelineStepDelegate interface {
	BuildStepDelegate
}
