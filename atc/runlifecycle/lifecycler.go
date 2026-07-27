package runlifecycle

import (
	"context"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
)

// Lifecycler is the pipeline_run_lifecycler RunnableComponent: it completes
// quiescent runs with a worst-of aggregate status, reopens completed runs
// that gained new builds (retriggers), and archives runs past their
// template's retention policy via the existing pipeline-archival machinery.
// For server-owned workflow-run templates that declare no run_retention, the
// platform retirement period acts as the default retention once the template
// is retired (workflow version superseded, all citing durable runs terminal);
// archiving those runs is what lets the tier-2 workflow-run template
// collector destroy the template, its runs, and its instances as one unit.
// These are generic run passes; the retired ticket-named per-ticket
// pipeline lifecycle (archiving the runs and templates of terminally-disposed
// tickets) is no longer part of the platform.
type Lifecycler struct {
	runFactory       db.PipelineRunFactory
	retirementPeriod time.Duration
}

func NewLifecycler(runFactory db.PipelineRunFactory, retirementPeriod time.Duration) *Lifecycler {
	return &Lifecycler{runFactory: runFactory, retirementPeriod: retirementPeriod}
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

	// The platform retirement window is the default retention for
	// server-owned workflow-run templates that declare no run_retention of
	// their own: once a template's workflow version is superseded and its
	// citing durable runs are all terminal, its runs archive here so the
	// tier-2 template collector can destroy the whole unit.
	if l.retirementPeriod > 0 {
		retired, err := l.runFactory.RunsOfRetiredTemplatesToArchive(l.retirementPeriod)
		if err != nil {
			logger.Error("failed-to-list-retired-template-runs", err)
			return err
		}
		for _, run := range retired {
			if err := run.Archive(); err != nil {
				logger.Error("failed-to-archive-retired-run", err, lager.Data{"run-id": run.ID()})
				continue
			}
			logger.Info("retired-run-archived", lager.Data{"run-id": run.ID()})
		}
	}

	return nil
}
