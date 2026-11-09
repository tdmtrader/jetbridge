package engine

import (
	"context"
	"io"

	"code.cloudfoundry.org/clock"
	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/exec"
	"github.com/concourse/concourse/atc/runtime"
)

func NewTaskDelegate(
	build db.Build,
	planID atc.PlanID,
	state exec.RunState,
	clock clock.Clock,
	dbWorkerFactory db.WorkerFactory,
	lockFactory lock.LockFactory,
) exec.TaskDelegate {
	return &taskDelegate{
		buildStepDelegate: NewBuildStepDelegate(build, planID, state, clock, atc.DisableRedactSecrets),

		eventOrigin: event.Origin{ID: event.OriginID(planID)},
		planID:      planID,
		build:       build,
		clock:       clock,

		dbWorkerFactory: dbWorkerFactory,
		lockFactory:     lockFactory,
	}
}

type taskDelegate struct {
	*buildStepDelegate

	planID      atc.PlanID
	config      atc.TaskConfig
	build       db.Build
	eventOrigin event.Origin
	clock       clock.Clock

	dbWorkerFactory db.WorkerFactory
	lockFactory     lock.LockFactory
}

func (d *taskDelegate) SetTaskConfig(config atc.TaskConfig) {
	d.config = config
}

// SidecarWriter is the writer of one sidecar's output. It is build output like
// the task's own and is redacted on the same terms, so it saves whole lines and
// holds an unterminated one back until a newline arrives, a later write comes
// more than a second after its last save, or it is closed. The runtime closes
// it when the sidecar's log stream ends (runtime.ProcessIO.SidecarWriters).
func (d *taskDelegate) SidecarWriter(sidecarName string) io.Writer {
	sidecarPlanID := atc.SidecarPlanID(d.planID, sidecarName)
	return d.logWriter(event.Origin{
		Source: event.OriginSourceStdout,
		ID:     event.OriginID(sidecarPlanID),
	})
}

func (d *taskDelegate) EmitSidecarPlans(logger lager.Logger, sidecars []atc.SidecarConfig) {
	for _, sc := range sidecars {
		sidecarPlan := atc.NewSidecarPlan(d.planID, sc)
		err := d.build.SaveEvent(event.Sidecar{
			Time: d.clock.Now().Unix(),
			Origin: event.Origin{
				ID: event.OriginID(d.planID),
			},
			PublicPlan: sidecarPlan.Public(),
		})
		if err != nil {
			logger.Error("failed-to-save-sidecar-event", err)
			return
		}
	}
}

func (d *taskDelegate) Initializing(logger lager.Logger) {
	err := d.build.SaveEvent(event.InitializeTask{
		Origin:     d.eventOrigin,
		Time:       d.clock.Now().Unix(),
		TaskConfig: d.shadowConfig(),
	})
	if err != nil {
		logger.Error("failed-to-save-initialize-task-event", err)
		return
	}

	logger.Info("initializing")
}

// shadowConfig is the task config the initialize-task and start-task events
// carry. It holds the task's command line, which the web UI and fly print as
// "running ...", so a credential interpolated into the path, args, dir or
// image is redacted on the same terms as the task's output.
func (d *taskDelegate) shadowConfig() event.TaskConfig {
	config := event.ShadowTaskConfig(d.config)
	if d.disableRedactSecrets {
		return config
	}
	config.Image = d.buildOutputFilter(config.Image)
	config.Run.Path = d.buildOutputFilter(config.Run.Path)
	config.Run.Dir = d.buildOutputFilter(config.Run.Dir)
	if config.Run.Args != nil {
		args := make([]string, len(config.Run.Args))
		for i, arg := range config.Run.Args {
			args[i] = d.buildOutputFilter(arg)
		}
		config.Run.Args = args
	}
	return config
}

func (d *taskDelegate) Starting(logger lager.Logger) {
	err := d.build.SaveEvent(event.StartTask{
		Origin:     d.eventOrigin,
		Time:       d.clock.Now().Unix(),
		TaskConfig: d.shadowConfig(),
	})
	if err != nil {
		logger.Error("failed-to-save-initialize-task-event", err)
		return
	}

	logger.Debug("starting")
}

func (d *taskDelegate) Finished(
	logger lager.Logger,
	exitStatus exec.ExitStatus,
) {
	// PR#4398: close to flush stdout and stderr
	d.Stdout().(io.Closer).Close()
	d.Stderr().(io.Closer).Close()

	err := d.build.SaveEvent(event.FinishTask{
		ExitStatus: int(exitStatus),
		Time:       d.clock.Now().Unix(),
		Origin:     d.eventOrigin,
	})
	if err != nil {
		logger.Error("failed-to-save-finish-event", err)
		return
	}

	logger.Info("finished", lager.Data{"exit-status": exitStatus})
}

func (d *taskDelegate) FetchImage(
	ctx context.Context,
	image atc.ImageResource,
	types atc.ResourceTypes,
	privileged bool,
	stepTags atc.Tags,
	skipInterval bool,
) (runtime.ImageSpec, error) {
	image.Name = "image"

	getPlan, checkPlan := atc.FetchImagePlan(d.planID, image, types, stepTags, skipInterval, nil)

	if checkPlan != nil {
		err := d.build.SaveEvent(event.ImageCheck{
			Time: d.clock.Now().Unix(),
			Origin: event.Origin{
				ID: event.OriginID(d.planID),
			},
			PublicPlan: checkPlan.Public(),
		})
		if err != nil {
			return runtime.ImageSpec{}, err
		}
	}

	err := d.build.SaveEvent(event.ImageGet{
		Time: d.clock.Now().Unix(),
		Origin: event.Origin{
			ID: event.OriginID(d.planID),
		},
		PublicPlan: getPlan.Public(),
	})
	if err != nil {
		return runtime.ImageSpec{}, err
	}

	imageSpec, _, err := d.buildStepDelegate.FetchImage(ctx, getPlan, checkPlan, privileged)
	if err != nil {
		return runtime.ImageSpec{}, err
	}

	return imageSpec, nil
}
