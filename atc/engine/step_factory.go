package engine

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/imageresolver"
	"github.com/concourse/concourse/atc/resource"
	"github.com/concourse/concourse/atc/worker"
)

type coreStepFactory struct {
	pool                  worker.Pool
	streamer              worker.Streamer
	lockFactory           lock.LockFactory
	teamFactory           db.TeamFactory
	buildFactory          db.BuildFactory
	resourceCacheFactory  db.ResourceCacheFactory
	resourceConfigFactory db.ResourceConfigFactory
	defaultLimits         atc.ContainerLimits
	defaultRequests       atc.ContainerLimits
	defaultCheckTimeout   time.Duration
	defaultGetTimeout     time.Duration
	defaultPutTimeout     time.Duration
	defaultTaskTimeout    time.Duration
	imageResolver         imageresolver.Resolver
	childRunAdmitter      exec.ChildRunAdmitter
}

// CoreStepFactoryOption configures optional fields on coreStepFactory.
type CoreStepFactoryOption func(*coreStepFactory)

// WithCoreImageResolver sets the image resolver for sidecar digest pinning.
func WithCoreImageResolver(r imageresolver.Resolver) CoreStepFactoryOption {
	return func(f *coreStepFactory) {
		f.imageResolver = r
	}
}

// WithChildRunAdmitter supplies the port the run_pipeline step admits through.
// Only the composition root passes it, because only the composition root may
// name the implementation; see atc/atccmd/child_run_admitter.go.
func WithChildRunAdmitter(admitter exec.ChildRunAdmitter) CoreStepFactoryOption {
	return func(f *coreStepFactory) {
		f.childRunAdmitter = admitter
	}
}

func NewCoreStepFactory(
	pool worker.Pool,
	streamer worker.Streamer,
	lockFactory lock.LockFactory,
	teamFactory db.TeamFactory,
	buildFactory db.BuildFactory,
	resourceCacheFactory db.ResourceCacheFactory,
	resourceConfigFactory db.ResourceConfigFactory,
	defaultLimits atc.ContainerLimits,
	defaultRequests atc.ContainerLimits,
	defaultCheckTimeout time.Duration,
	defaultGetTimeout time.Duration,
	defaultPutTimeout time.Duration,
	defaultTaskTimeout time.Duration,
	opts ...CoreStepFactoryOption,
) CoreStepFactory {
	f := &coreStepFactory{
		pool:                  pool,
		streamer:              streamer,
		lockFactory:           lockFactory,
		teamFactory:           teamFactory,
		buildFactory:          buildFactory,
		resourceCacheFactory:  resourceCacheFactory,
		resourceConfigFactory: resourceConfigFactory,
		defaultLimits:         defaultLimits,
		defaultRequests:       defaultRequests,
		defaultCheckTimeout:   defaultCheckTimeout,
		defaultGetTimeout:     defaultGetTimeout,
		defaultPutTimeout:     defaultPutTimeout,
		defaultTaskTimeout:    defaultTaskTimeout,
	}
	for _, opt := range opts {
		opt(f)
	}
	return f
}

func (factory *coreStepFactory) GetStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = resource.ResourcesDir("get")

	getStep := exec.NewGetStep(
		plan.ID,
		*plan.Get,
		stepMetadata,
		containerMetadata,
		factory.lockFactory,
		factory.resourceCacheFactory,
		delegateFactory,
		factory.pool,
		factory.defaultGetTimeout,
	)

	getStep = exec.LogError(getStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		getStep = exec.RetryError(getStep, delegateFactory)
	}
	return getStep
}

func (factory *coreStepFactory) PutStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = resource.ResourcesDir("put")

	putStep := exec.NewPutStep(
		plan.ID,
		*plan.Put,
		stepMetadata,
		containerMetadata,
		factory.pool,
		delegateFactory,
		factory.defaultPutTimeout,
	)

	putStep = exec.LogError(putStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		putStep = exec.RetryError(putStep, delegateFactory)
	}
	return putStep
}

func (factory *coreStepFactory) CheckStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = resource.ResourcesDir("check")

	var checkOpts []exec.CheckStepOption
	if factory.imageResolver != nil {
		checkOpts = append(checkOpts, exec.WithCheckResolver(factory.imageResolver))
	}

	checkStep := exec.NewCheckStep(
		plan.ID,
		*plan.Check,
		stepMetadata,
		factory.resourceConfigFactory,
		containerMetadata,
		factory.pool,
		delegateFactory,
		factory.defaultCheckTimeout,
		checkOpts...,
	)

	checkStep = exec.LogError(checkStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		checkStep = exec.RetryError(checkStep, delegateFactory)
	}
	return checkStep
}

func (factory *coreStepFactory) RunStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	containerMetadata.WorkingDirectory = "/tmp/build/run"

	runStep := exec.NewRunStep(
		plan.ID,
		*plan.Run,
		delegateFactory,
	)

	runStep = exec.LogError(runStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		runStep = exec.RetryError(runStep, delegateFactory)
	}
	return runStep
}

func (factory *coreStepFactory) TaskStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	containerMetadata db.ContainerMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	sum := sha256.Sum256([]byte(plan.Task.Name))
	containerMetadata.WorkingDirectory = filepath.Join("/tmp", "build", fmt.Sprintf("%x", sum[:4]))

	var taskOpts []exec.TaskStepOption
	if factory.imageResolver != nil {
		taskOpts = append(taskOpts, exec.WithImageResolver(factory.imageResolver))
	}

	taskStep := exec.NewTaskStep(
		plan.ID,
		*plan.Task,
		factory.defaultLimits,
		factory.defaultRequests,
		stepMetadata,
		containerMetadata,
		factory.pool,
		factory.streamer,
		delegateFactory,
		factory.defaultTaskTimeout,
		taskOpts...,
	)

	taskStep = exec.LogError(taskStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		taskStep = exec.RetryError(taskStep, delegateFactory)
	}
	return taskStep
}

func (factory *coreStepFactory) SetPipelineStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	spStep := exec.NewSetPipelineStep(
		plan.ID,
		*plan.SetPipeline,
		stepMetadata,
		delegateFactory,
		factory.teamFactory,
		factory.buildFactory,
		factory.streamer,
	)

	spStep = exec.LogError(spStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		spStep = exec.RetryError(spStep, delegateFactory)
	}
	return spStep
}

func (factory *coreStepFactory) RunPipelineStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	rpStep := exec.NewRunPipelineStep(
		plan.ID,
		*plan.RunPipeline,
		stepMetadata,
		delegateFactory,
		factory.admitterOrFallback(),
	)

	rpStep = exec.LogError(rpStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		rpStep = exec.RetryError(rpStep, delegateFactory)
	}
	return rpStep
}

// admitterOrFallback is the admitter the run_pipeline step is built over, or
// a stand-in that refuses.
//
// The option is optional, and deliberately so: every test that builds a core
// factory, and the brine harness that builds one to drive real plans through
// the real engine, construct it without an admitter because they have no
// composition root to get one from. Handing those a nil interface would turn
// the first run_pipeline plan any of them ever carries into a nil dereference
// inside the step, which reports the wiring gap as a panic on a worker rather
// than as a failed step with a reason. So the fallback is a value, and it says
// what is actually wrong.
func (factory *coreStepFactory) admitterOrFallback() exec.ChildRunAdmitter {
	if factory.childRunAdmitter == nil {
		return unwiredChildRunAdmitter{}
	}

	return factory.childRunAdmitter
}

// unwiredChildRunAdmitter refuses every admission on a web node where the
// run_pipeline port was never supplied. It fails the step rather than the
// build's platform, because exec treats an admitter's refusal as a step
// failure with the message on stderr, which is where the operator will see it.
type unwiredChildRunAdmitter struct{}

func (unwiredChildRunAdmitter) AdmitChildRun(context.Context, exec.ChildRunRequest) (exec.ChildRun, error) {
	return exec.ChildRun{}, errors.New("run_pipeline is not wired on this web node")
}

func (factory *coreStepFactory) LoadVarStep(
	plan atc.Plan,
	stepMetadata exec.StepMetadata,
	delegateFactory DelegateFactory,
) exec.Step {
	loadVarStep := exec.NewLoadVarStep(
		plan.ID,
		*plan.LoadVar,
		stepMetadata,
		delegateFactory,
		factory.streamer,
	)

	loadVarStep = exec.LogError(loadVarStep, delegateFactory)
	if atc.EnableBuildRerunWhenWorkerDisappears {
		loadVarStep = exec.RetryError(loadVarStep, delegateFactory)
	}
	return loadVarStep
}

func (factory *coreStepFactory) ArtifactInputStep(
	plan atc.Plan,
	build db.Build,
) exec.Step {
	return exec.NewArtifactInputStep(plan, build, factory.pool)
}

func (factory *coreStepFactory) ArtifactOutputStep(
	plan atc.Plan,
	build db.Build,
) exec.Step {
	return exec.NewArtifactOutputStep(plan, build, factory.pool)
}
