package runlifecycle

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
)

// Lifecycler is the pipeline_run_lifecycler RunnableComponent: it completes
// quiescent runs with a worst-of aggregate status, reopens completed runs
// that gained new builds (retriggers), and archives runs past their
// template's retention policy via the existing pipeline-archival machinery.
// These are generic run passes; the retired ticket-named per-ticket
// pipeline lifecycle (archiving the runs and templates of terminally-disposed
// tickets) is no longer part of the platform.
type Lifecycler struct {
	runFactory db.PipelineRunFactory
}

func NewLifecycler(runFactory db.PipelineRunFactory) *Lifecycler {
	return &Lifecycler{runFactory: runFactory}
}

func (l *Lifecycler) Run(ctx context.Context) error {
	logger := lagerctx.FromContext(ctx).Session("pipeline-run-lifecycler")

	running, err := l.runFactory.RunningRuns()
	if err != nil {
		logger.Error("failed-to-list-running-runs", err)
		return err
	}

	for _, run := range running {
		status, complete, err := run.CheckComplete()
		if err != nil {
			logger.Error("failed-to-check-run-completion", err, lager.Data{"run-id": run.ID()})
			continue
		}
		if !complete {
			continue
		}
		if err := run.Finish(status); err != nil {
			logger.Error("failed-to-finish-run", err, lager.Data{"run-id": run.ID()})
			continue
		}
		logger.Info("run-completed", lager.Data{"run-id": run.ID(), "status": string(status)})
	}

	reactivated, err := l.runFactory.CompletedRunsWithNewActivity()
	if err != nil {
		logger.Error("failed-to-list-reactivated-runs", err)
		return err
	}
	for _, run := range reactivated {
		if err := run.Reopen(); err != nil {
			logger.Error("failed-to-reopen-run", err, lager.Data{"run-id": run.ID()})
			continue
		}
		logger.Info("run-reopened", lager.Data{"run-id": run.ID()})
	}

	expired, err := l.runFactory.RunsToArchive()
	if err != nil {
		logger.Error("failed-to-list-expired-runs", err)
		return err
	}
	for _, run := range expired {
		if err := run.Archive(); err != nil {
			logger.Error("failed-to-archive-run", err, lager.Data{"run-id": run.ID()})
			continue
		}
		logger.Info("run-archived", lager.Data{"run-id": run.ID()})
	}

	return nil
}
