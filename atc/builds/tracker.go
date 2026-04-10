package builds

import (
	"context"
	"fmt"
	"sync"

	"code.cloudfoundry.org/lager/v3"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/atc/util"
	"github.com/concourse/concourse/tracing"
)

//go:generate go run github.com/maxbrunsfeld/counterfeiter/v6 -generate

//counterfeiter:generate . Engine
type Engine interface {
	NewBuild(db.Build) Runnable

	Drain(context.Context)
}

//counterfeiter:generate . Runnable
type Runnable interface {
	Run(context.Context)
}

func NewTracker(
	logger lager.Logger,
	buildFactory db.BuildFactory,
	engine Engine,
	checkBuildsChan <-chan db.Build,
) *Tracker {
	tracker := &Tracker{
		buildFactory:    buildFactory,
		engine:          engine,
		running:         &sync.Map{},
		checkBuildsChan: checkBuildsChan,
	}
	go tracker.trackInMemoryBuilds(logger)
	return tracker
}

type Tracker struct {
	buildFactory db.BuildFactory
	engine       Engine

	checkBuildsChan <-chan db.Build

	running *sync.Map
}

func (bt *Tracker) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx)

	logger.Debug("start")
	defer logger.Debug("done")

	builds, err := bt.buildFactory.GetAllStartedBuilds()
	if err != nil {
		logger.Error("failed-to-lookup-started-builds", err)
		return err
	}

	for _, b := range builds {
		bt.trackBuild(logger, b)
	}

	return nil
}

func (bt *Tracker) Drain(ctx context.Context) {
	bt.engine.Drain(ctx)
}

func (bt *Tracker) trackBuild(logger lager.Logger, b db.Build) {
	var id string
	if b.ID() != 0 {
		id = fmt.Sprintf("build-%d", b.ID())
	} else {
		id = fmt.Sprintf("resource-%d", b.ResourceID())
	}

	if _, exists := bt.running.LoadOrStore(id, true); exists {
		return
	}

	go func(build db.Build, id string) {
		loggerData := build.LagerData()
		defer func() {
			err := util.DumpPanic(recover(), "tracking build %d", build.ID())
			if err != nil {
				logger.Error("panic-in-tracker-build-run", err)

				build.Finish(db.BuildStatusErrored)
			} else if build.IsRunning() {
				// Build exited Run() without calling Finish (e.g. lock
				// acquisition error, engine drain). Finalize it so that
				// in-flight check tracking is cleared and the resource
				// is not permanently blocked from future checks.
				logger.Info("finalizing-orphaned-build", loggerData)
				build.Finish(db.BuildStatusErrored)
			}
		}()

		defer bt.running.Delete(id)

		if build.Name() == db.CheckBuildName {
			metric.Metrics.CheckBuildsRunning.Inc()
			defer metric.Metrics.CheckBuildsRunning.Dec()
		} else {
			metric.Metrics.BuildsRunning.Inc()
			defer metric.Metrics.BuildsRunning.Dec()
		}

		ctx := context.Background()
		ctx, span := tracing.StartSpanFollowing(ctx, build, "build-tracker.track", build.TracingAttrs())
		defer span.End()

		bt.engine.NewBuild(build).Run(
			lagerctx.NewContext(
				ctx,
				logger.Session("run", loggerData),
			),
		)
	}(b, id)
}

func (bt *Tracker) trackInMemoryBuilds(logger lager.Logger) {
	logger = logger.Session("tracker-imb")
	logger.Info("start")
	defer logger.Info("end")

	for {
		select {
		case b := <-bt.checkBuildsChan:
			if b == nil {
				return
			}
			logger.Debug("received-in-memory-build", b.LagerData())
			bt.trackBuild(logger, b)
		}
	}
}
