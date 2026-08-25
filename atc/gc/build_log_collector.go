package gc

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
)

type buildLogCollector struct {
	pipelineFactory             db.PipelineFactory
	pipelineLifecycle           db.PipelineLifecycle
	batchSize                   int
	drainerConfigured           bool
	buildLogRetentionCalculator BuildLogRetentionCalculator
}

func NewBuildLogCollector(
	pipelineFactory db.PipelineFactory,
	pipelineLifecycle db.PipelineLifecycle,
	batchSize int,
	buildLogRetentionCalculator BuildLogRetentionCalculator,
	drainerConfigured bool,
) *buildLogCollector {
	return &buildLogCollector{
		pipelineFactory:             pipelineFactory,
		pipelineLifecycle:           pipelineLifecycle,
		batchSize:                   batchSize,
		drainerConfigured:           drainerConfigured,
		buildLogRetentionCalculator: buildLogRetentionCalculator,
	}
}

func (br *buildLogCollector) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("build-reaper")

	logger.Debug("start")
	defer logger.Debug("done")

	err := br.pipelineLifecycle.RemoveBuildEventsForDeletedPipelines()
	if err != nil {
		logger.Error("failed-to-remove-build-events-for-deleted-pipelines", err)
		return err
	}

	pipelines, err := br.pipelineFactory.AllPipelines()
	if err != nil {
		logger.Error("failed-to-get-pipelines", err)
		return err
	}

	for _, pipeline := range pipelines {
		if pipeline.Paused() {
			continue
		}

		jobs, err := pipeline.Jobs()
		if err != nil {
			logger.Error("failed-to-get-dashboard", err)
			continue
		}

		for _, job := range jobs {
			if job.Paused() {
				continue
			}

			builds := job.ChronoBuilds
			deleteEvents := pipeline.DeleteBuildEventsByBuildIDs
			if pipeline.Template() {
				builds = func(page db.Page) ([]db.BuildForAPI, db.Pagination, error) {
					return pipeline.ChronoRunBuilds(job.Name(), page)
				}
				deleteEvents = pipeline.DeleteRunBuildEventsByBuildIDs
			}

			err = br.reapLogsOfJob(job, builds, deleteEvents, logger)
			if err != nil {
				continue
			}
		}
	}

	return nil
}

func (br *buildLogCollector) reapLogsOfJob(
	job db.Job,
	builds func(db.Page) ([]db.BuildForAPI, db.Pagination, error),
	deleteEvents func([]int) error,
	logger lager.Logger) error {

	jobConfig, err := job.Config()
	if err != nil {
		logger.Error("failed-to-get-job-config", err)
		return err
	}

	logRetention := br.buildLogRetentionCalculator.BuildLogsToRetain(jobConfig)
	if logRetention.Builds == 0 && logRetention.Days == 0 {
		return nil
	}

	buildsToConsiderDeleting := []db.BuildForAPI{}

	from := job.FirstLoggedBuildID()
	limit := br.batchSize
	page := &db.Page{From: &from, Limit: limit}
	for page != nil {
		pageBuilds, pagination, err := builds(*page)
		if err != nil {
			logger.Error("failed-to-get-job-builds-to-delete", err)
			return err
		}

		buildsOfBatch := []db.BuildForAPI{}
		for _, build := range pageBuilds {
			// Ignore reaped builds
			if !build.ReapTime().IsZero() {
				continue
			}

			buildsOfBatch = append(buildsOfBatch, build)
		}
		buildsToConsiderDeleting = append(buildsOfBatch, buildsToConsiderDeleting...)

		page = pagination.Newer
	}

	logger.Debug("after-first-round-filter", lager.Data{
		"builds_to_consider_deleting": len(buildsToConsiderDeleting),
	})

	if len(buildsToConsiderDeleting) == 0 {
		return nil
	}

	buildIDsToDelete := []int{}
	candidateBuildIDsToKeep := []int{}
	retainedBuilds := 0
	retainedSucceededBuilds := 0
	firstLoggedBuildID := 0
	for _, build := range buildsToConsiderDeleting {
		// Running build should not be reaped.
		if build.IsRunning() {
			firstLoggedBuildID = build.ID()
			continue
		}

		// Before a build is drained, it should not be reaped.
		if br.drainerConfigured {
			if !build.IsDrained() {
				firstLoggedBuildID = build.ID()
				continue
			}
		}

		maxBuildsRetained := retainedBuilds >= logRetention.Builds
		// INVARIANT: 0 <= logRetention.Days <= gc.MaxRetentionDays. The calculator
		// is the only producer of a retention, and it bounds all three sources of
		// Days (both operator flags and the job's own declaration) into that range
		// -- because BOTH ends of it delete everything here. A negative Days makes
		// this line true for every build ever run; a Days near MaxInt overflows
		// AddDate's `day + days` into an arbitrary instant that reads the same way.
		// Neither says a word on the way out.
		buildHasExpired := !build.EndTime().IsZero() && build.EndTime().AddDate(0, 0, logRetention.Days).Before(time.Now())

		if logRetention.Builds != 0 {
			if logRetention.MinimumSucceededBuilds != 0 {
				if build.Status() == db.BuildStatusSucceeded && retainedSucceededBuilds < logRetention.MinimumSucceededBuilds {
					retainedBuilds++
					retainedSucceededBuilds++
					firstLoggedBuildID = build.ID()
					continue
				}
			}

			if !maxBuildsRetained {
				retainedBuilds++
				candidateBuildIDsToKeep = append(candidateBuildIDsToKeep, build.ID())
				firstLoggedBuildID = build.ID()
				continue
			}
		}

		if logRetention.Days != 0 {
			if !buildHasExpired {
				retainedBuilds++
				candidateBuildIDsToKeep = append(candidateBuildIDsToKeep, build.ID())
				firstLoggedBuildID = build.ID()
				continue
			}
		}

		// at this point, we haven't met all of the enabled conditions, so here we can reap
		buildIDsToDelete = append(buildIDsToDelete, build.ID())

	}

	logger.Debug("after-second-round-filter", lager.Data{
		"retained_builds":           retainedBuilds,
		"retained_succeeded_builds": retainedSucceededBuilds,
	})

	if len(buildIDsToDelete) == 0 {
		logger.Debug("no-builds-to-reap")
		return nil
	}

	// If we exceeded the maximum number of builds we should delete the oldest
	// candidates. This correction only ever fires when the minimum-succeeded arm
	// retained past the count.
	//
	// INVARIANT: delta <= len(candidateBuildIDsToKeep), so the indexing below
	// cannot run off the front of the keep list. Writing M for the retentions the
	// min-succeeded arm took and K for len(candidateBuildIDsToKeep):
	//
	//   - the min-succeeded arm is the ONLY retention that does not append to the
	//     keep list, and it is gated on
	//     retainedSucceededBuilds < MinimumSucceededBuilds, so
	//     M <= logRetention.MinimumSucceededBuilds;
	//   - every other retention (count arm, days arm) appends, so
	//     retainedBuilds == M + K;
	//   - hence delta == retainedBuilds - Builds == M + K - Builds, and
	//     delta <= K exactly when M <= Builds.
	//
	// The bound therefore rests entirely on two things the CALCULATOR owes this
	// site, neither of them checkable from here: MinimumSucceededBuilds <= Builds,
	// which BuildLogsToRetain applies in BOTH of its branches (when only one of
	// them did, an ordinary job config panicked this line on index [-1]), and
	// Builds > 0, which is why the uint64 operator flags are saturated rather than
	// truncated into int.
	//
	// A defensive clamp of delta was considered and REJECTED: with the
	// calculator's bounds in place it has no reachable input, and it would
	// silently absorb precisely the breakage this contract exists to make loud --
	// clamped, the reaper would go on quietly deleting logs it was asked to keep.
	if logRetention.Builds != 0 && retainedBuilds > logRetention.Builds {
		logger.Debug("more-builds-to-retain", lager.Data{
			"retained_builds": retainedBuilds,
		})
		delta := retainedBuilds - logRetention.Builds
		n := len(candidateBuildIDsToKeep)
		for i := 1; i <= delta; i++ {
			buildIDsToDelete = append(buildIDsToDelete, candidateBuildIDsToKeep[n-i])
		}
	}

	logger.Debug("reaping-builds", lager.Data{
		"build_ids": buildIDsToDelete,
	})

	err = deleteEvents(buildIDsToDelete)
	if err != nil {
		logger.Error("failed-to-delete-build-events", err)
		return err
	}

	if firstLoggedBuildID > job.FirstLoggedBuildID() {
		err = job.UpdateFirstLoggedBuildID(firstLoggedBuildID)
		if err != nil {
			logger.Error("failed-to-update-first-logged-build-id", err)
			return err
		}
	}

	return nil
}
