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
	opts ...DelegateOption,
) exec.TaskDelegate {
	return &taskDelegate{
		BuildStepDelegate: NewBuildStepDelegate(build, planID, state, clock, atc.DisableRedactSecrets, opts...),

		eventOrigin: event.Origin{ID: event.OriginID(planID)},
		planID:      planID,
		build:       build,
		clock:       clock,

		dbWorkerFactory: dbWorkerFactory,
		lockFactory:     lockFactory,
	}
}

type taskDelegate struct {
	exec.BuildStepDelegate

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

func (d *taskDelegate) SidecarWriter(sidecarName string) io.Writer {
	sidecarPlanID := atc.SidecarPlanID(d.planID, sidecarName)
	return newDBEventWriter(
		d.build,
		event.Origin{
			Source: event.OriginSourceStdout,
			ID:     event.OriginID(sidecarPlanID),
		},
		d.clock,
		nil,  // no build output filter for sidecar logs
		true, // skip secret redaction (avoids nil filter call)
	)
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
		TaskConfig: event.ShadowTaskConfig(d.config),
	})
	if err != nil {
		logger.Error("failed-to-save-initialize-task-event", err)
		return
	}

	logger.Info("initializing")
}

func (d *taskDelegate) Starting(logger lager.Logger) {
	err := d.build.SaveEvent(event.StartTask{
		Origin:     d.eventOrigin,
		Time:       d.clock.Now().Unix(),
		TaskConfig: event.ShadowTaskConfig(d.config),
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

// FetchImage resolves a task's image_resource: a registry-image reference
// the build step delegate pins natively. Nothing runs as a step and no event
// is emitted for it; the task's own events carry the image it ran in.
func (d *taskDelegate) FetchImage(
	ctx context.Context,
	image atc.ImageResource,
	privileged bool,
	stepTags atc.Tags,
) (runtime.ImageSpec, error) {
	image.Name = "image"

	imageSpec, _, err := d.BuildStepDelegate.FetchImage(ctx, atc.FetchImagePlan(d.planID, image, stepTags), privileged)
	if err != nil {
		return runtime.ImageSpec{}, err
	}

	return imageSpec, nil
}
