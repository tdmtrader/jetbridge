package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/builds"
	"github.com/concourse/concourse/atc/creds"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/tracing"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

func NewEngine(
	stepperFactory StepperFactory,
	secrets creds.Secrets,
	varSourcePool creds.VarSourcePool,
) Engine {
	return Engine{
		stepperFactory: stepperFactory,
		release:        make(chan bool),
		trackedStates:  new(sync.Map),
		waitGroup:      new(sync.WaitGroup),

		globalSecrets: secrets,
		varSourcePool: varSourcePool,
	}
}

type Engine struct {
	stepperFactory StepperFactory
	release        chan bool
	trackedStates  *sync.Map
	waitGroup      *sync.WaitGroup

	globalSecrets creds.Secrets
	varSourcePool creds.VarSourcePool
}

func (engine Engine) Drain(ctx context.Context) {
	logger := lagerctx.FromContext(ctx)

	logger.Info("start")
	defer logger.Info("done")

	close(engine.release)

	logger.Info("waiting")

	engine.waitGroup.Wait()
}

func (engine Engine) NewBuild(build db.Build) builds.Runnable {
	return NewBuild(
		build,
		engine.stepperFactory,
		engine.globalSecrets,
		engine.varSourcePool,
		engine.release,
		engine.trackedStates,
		engine.waitGroup,
	)
}

func NewBuild(
	build db.Build,
	builder StepperFactory,
	globalSecrets creds.Secrets,
	varSourcePool creds.VarSourcePool,
	release chan bool,
	trackedStates *sync.Map,
	waitGroup *sync.WaitGroup,
) builds.Runnable {
	return &engineBuild{
		build:   build,
		builder: builder,

		globalSecrets: globalSecrets,
		varSourcePool: varSourcePool,

		release:       release,
		trackedStates: trackedStates,
		waitGroup:     waitGroup,
	}
}

type engineBuild struct {
	build   db.Build
	builder StepperFactory

	globalSecrets creds.Secrets
	varSourcePool creds.VarSourcePool

	release       chan bool
	trackedStates *sync.Map
	waitGroup     *sync.WaitGroup
}

func (b *engineBuild) Run(ctx context.Context) {
	b.waitGroup.Add(1)
	defer b.waitGroup.Done()

	logger := lagerctx.FromContext(ctx).WithData(b.build.LagerData())

	lock, acquired, err := b.build.AcquireTrackingLock(logger, time.Minute)
	if err != nil {
		logger.Error("failed-to-get-lock", err)
		return
	}

	if !acquired {
		logger.Debug("build-already-tracked")
		return
	}

	defer lock.Release()

	found, err := b.build.Reload()
	if err != nil {
		logger.Error("failed-to-load-build-from-db", err)
		return
	}

	if !found {
		logger.Info("build-not-found")
		return
	}

	if !b.build.IsRunning() {
		logger.Info("build-already-finished")
		return
	}

	ctx, span := tracing.StartSpanFollowing(ctx, b.build, "build", b.build.TracingAttrs())
	defer span.End()

	stepper, err := b.builder.StepperForBuild(b.build)
	if err != nil {
		logger.Error("failed-to-construct-build-stepper", err)

		// Fails the build if BuildStep returned an error because such unrecoverable
		// errors will cause a build to never start to run. finish emits the
		// error event (no step ran, so nothing else will).
		b.finish(logger.Session("finish"), err, false, false)

		return
	}

	b.trackStarted(logger)
	defer b.trackFinished(logger)

	logger.Info("running")

	state, err := b.runState(logger, stepper)
	if err != nil {
		logger.Error("failed-to-create-run-state", err)

		// Fails the build if fetching the pipeline variables fails, as these errors
		// are unrecoverable - e.g. if pipeline var_sources is wrong. finish
		// emits the error event (no step ran, so nothing else will).
		b.finish(logger.Session("finish"), err, false, false)

		return
	}
	defer b.clearRunState()

	abortSignal, unlisten, err := b.build.AbortSignal()
	if err != nil {
		logger.Error("failed-to-listen-for-aborts", err)
		return
	}

	if abortSignal != nil {
		defer unlisten()

		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)

		noleak := make(chan bool)
		defer close(noleak)

		// The signal only fires for aborts that arrive after ListenSignal, so
		// read the flag once here: a build aborted while no web was tracking
		// it — across a restart, or before a re-track following a retriable
		// step error — is invisible to the listener and would otherwise run
		// to completion. Read on this goroutine, before the plan starts, so
		// that an already-aborted build never gets as far as creating a pod.
		if b.abortRequested(logger) {
			logger.Info("aborting")
			cancel()
		} else {
			go func() {
				// NotifySignal coalesces: a wake-up says only that something
				// changed, so the flag is re-read on every one of them. A
				// single check would let the first wake-up that is not an
				// abort — a listener reconnect, or a read that failed — cost
				// the build every abort that follows.
				for {
					select {
					case <-noleak:
						return
					case <-abortSignal.C():
						if b.abortRequested(logger) {
							logger.Info("aborting")
							cancel()
							return
						}
					}
				}
			}()
		}
	}

	var succeeded bool
	var runErr error
	// A recovered panic (or any error not raised by a running step) is not
	// surfaced by a step's LogError wrapper, so finish() must emit the error
	// event for it; a normal step error already flowed through LogError and
	// must NOT be re-emitted (see finish()).
	var panicked bool

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			err := util.DumpPanic(recover(), "running build plan %d", b.build.ID())
			if err != nil {
				logger.Error("panic-in-engine-build-step-run", err)
				runErr = err
				panicked = true
			}
		}()
		succeeded, runErr = state.Run(lagerctx.NewContext(ctx, tracing.LoggerWithSpan(ctx, logger)), b.build.PrivatePlan())
	}()

	select {
	case <-b.release:
		logger.Info("releasing")

		// In-memory check builds cannot resume across a restart, so finalize
		// them to clear in-flight check tracking. Job builds are left in
		// "started" so the next web's build tracker re-attaches to them.
		if b.build.Name() == db.CheckBuildName {
			b.finish(logger.Session("finish"), fmt.Errorf("build released during drain"), false, false)
		}

	case <-done:
		// Don't retry check build because if a check build drops into endless retry,
		// there is no way to abort it.
		if b.build.Name() != db.CheckBuildName && errors.As(runErr, &exec.Retriable{}) {
			return
		}

		// An in-memory build only generates a real build id once start to run,
		// so let's update logger with the latest lager data. A non-panic runErr
		// on the done path came from a running step (already surfaced by its
		// LogError wrapper on job builds); a panic did not.
		b.finish(logger.Session("finish").WithData(b.build.LagerData()), runErr, succeeded, !panicked)
	}
}

// abortRequested reports whether the build has been marked as aborted. It
// reads the flag from the database rather than trusting the in-memory row,
// which is stale from when the build was first loaded, and rather than
// Reload, which would write that row while the goroutine running the plan is
// reading it. A failed read reports "not aborted": the caller is watching a
// signal that will bring it back here.
func (b *engineBuild) abortRequested(logger lager.Logger) bool {
	aborted, err := b.build.IsAbortedInDB()
	if err != nil {
		logger.Error("failed-to-read-aborted-flag", err)
		return false
	}

	return aborted
}

func (b *engineBuild) buildStepErrored(logger lager.Logger, message string) {
	err := b.build.SaveEvent(event.Error{
		Message: message,
		Origin: event.Origin{
			ID: event.OriginID(b.build.PrivatePlan().ID),
		},
		Time: time.Now().Unix(),
	})
	if err != nil {
		logger.Error("failed-to-save-error-event", err)
	}
}

// finish records the build's terminal status and, for errors, surfaces the
// reason in the build output. fromRunningStep is true when err was returned by
// a running step on the done path: on job builds that error already flowed
// through the step's LogError wrapper (emitted at the leaf origin), so
// re-emitting here would render a duplicate. Check builds are not
// LogError-wrapped, and pre-step / drain / panic errors never reach a wrapper,
// so those still emit here (review finding, 2026-07-12).
func (b *engineBuild) finish(logger lager.Logger, err error, succeeded bool, fromRunningStep bool) {
	if errors.Is(err, context.Canceled) {
		b.saveStatus(logger, atc.StatusAborted)
		logger.Info("aborted")

	} else if err != nil {
		// Surface the error in the build output. Without this, an errored
		// build (a check build especially) shows no reason in the UI — the
		// message would otherwise only exist in the web process log.
		message := err.Error()
		var retriable exec.Retriable
		if errors.As(err, &retriable) {
			message = retriable.Cause.Error()
		}
		if !fromRunningStep || b.build.Name() == db.CheckBuildName {
			b.buildStepErrored(logger, message)
		}

		b.saveStatus(logger, atc.StatusErrored)
		logger.Info("errored", lager.Data{"error": err.Error()})

	} else if succeeded {
		b.saveStatus(logger, atc.StatusSucceeded)
		logger.Info("succeeded")

	} else {
		b.saveStatus(logger, atc.StatusFailed)
		logger.Info("failed")
	}
}

func (b *engineBuild) saveStatus(logger lager.Logger, status atc.BuildStatus) {
	if err := b.build.Finish(db.BuildStatus(status)); err != nil {
		logger.Error("failed-to-finish-build", err)
		return
	}

	if b.build.JobID() == 0 {
		return
	}

	job, found, err := b.build.Job()
	if err != nil {
		logger.Error("failed-to-get-job", err)
		return
	}
	if !found {
		logger.Info("build-job-not-found")
		return
	}

	id, err := job.LatestCompletedBuildId()
	if err != nil {
		logger.Error("failed-to-get-latest-completed-build-id", err)
		return
	}
	if b.build.ID() >= id {
		metric.JobStatus{
			Status:       status.String(),
			JobName:      job.Name(),
			PipelineName: job.PipelineName(),
			TeamName:     job.TeamName(),
		}.Emit(logger)
	}
}

func (b *engineBuild) trackStarted(logger lager.Logger) {
	if b.build.Name() != db.CheckBuildName {
		metric.BuildStarted{
			Build: b.build,
		}.Emit(logger)
	}
}

func (b *engineBuild) trackFinished(logger lager.Logger) {
	found, err := b.build.Reload()
	if err != nil {
		logger.Error("failed-to-load-build-from-db", err)
		return
	}

	if !found {
		logger.Info("build-removed")
		return
	}

	if !b.build.IsRunning() {
		if b.build.Name() != db.CheckBuildName {
			metric.BuildFinished{
				Build: b.build,
			}.Emit(logger)
		}
	}
}

func (b *engineBuild) runState(logger lager.Logger, stepper exec.Stepper) (exec.RunState, error) {
	id := b.build.RunStateID()
	existingState, ok := b.trackedStates.Load(id)
	if ok {
		return existingState.(exec.RunState), nil
	}
	credVars, err := b.build.Variables(logger, b.globalSecrets, b.varSourcePool)
	if err != nil {
		return nil, err
	}
	state, _ := b.trackedStates.LoadOrStore(id, exec.NewRunState(stepper, credVars))
	return state.(exec.RunState), nil
}

func (b *engineBuild) clearRunState() {
	b.trackedStates.Delete(b.build.RunStateID())
}
