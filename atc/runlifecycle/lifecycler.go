package runlifecycle

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/concourse/concourse/atc/db"
)

// Lifecycler is the pipeline_run_lifecycler RunnableComponent: it completes
// quiescent runs with a worst-of aggregate status, reopens completed runs
// that gained new builds (retriggers), archives runs past their template's
// retention policy via the existing pipeline-archival machinery, and
// archives the runs and templates of terminally-disposed agent tickets (C3,
// UI audit 2026-07-17) — dead dashboard cards otherwise. Archiving lives
// here, not in a ticket-transition hook, so it catches every Transition
// writer (HTTP, dispatch, harvest, future reconcilers), survives web
// restarts between transition and archive, and can never block or fail a
// transition; terminal states have no outgoing edges, so it never races a
// requeue.
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

	ticketRuns, err := l.runFactory.RunsForTerminalTickets()
	if err != nil {
		logger.Error("failed-to-list-terminal-ticket-runs", err)
		return err
	}
	for _, run := range ticketRuns {
		if err := run.Archive(); err != nil {
			logger.Error("failed-to-archive-terminal-ticket-run", err, lager.Data{"run-id": run.ID()})
			continue
		}
		logger.Info("run-archived-terminal-ticket", lager.Data{"run-id": run.ID()})
	}

	templates, err := l.runFactory.TemplatesForTerminalTickets()
	if err != nil {
		logger.Error("failed-to-list-terminal-ticket-templates", err)
		return err
	}
	for _, pipeline := range templates {
		if err := pipeline.Archive(); err != nil {
			logger.Error("failed-to-archive-terminal-ticket-template", err, lager.Data{"pipeline-id": pipeline.ID(), "pipeline-name": pipeline.Name()})
			continue
		}
		logger.Info("template-archived-terminal-ticket", lager.Data{"pipeline-id": pipeline.ID(), "pipeline-name": pipeline.Name()})
	}

	return nil
}
