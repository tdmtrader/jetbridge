package db

import (
	"context"
	"errors"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc/db/lock"
)

// pipelinePauserAttribution is the paused_by this sweep writes. It is a
// platform attribution, not a user one: reopenPipelineRun clears it alongside
// 'run-completed', so a run payload auto-paused here is always recoverable --
// by pipeline.Unpause while the run is still 'running', and by a manual job
// trigger once it is terminal. Do not reuse this string for a pause a user
// asked for; a user pause is meant to survive reopen.
const pipelinePauserAttribution = "automatic-pipeline-pauser"

type PipelinePauser interface {
	PausePipelines(ctx context.Context, daysSinceLastBuild int) error
}

func NewPipelinePauser(conn DbConn, lockFactory lock.LockFactory) PipelinePauser {
	return &pipelinePauser{
		conn:        conn,
		lockFactory: lockFactory,
	}
}

type pipelinePauser struct {
	conn        DbConn
	lockFactory lock.LockFactory
}

func (p *pipelinePauser) PausePipelines(ctx context.Context, daysSinceLastBuild int) error {
	logger := lagerctx.FromContext(ctx).Session("pipeline-pauser")
	rows, err := pipelinesQuery.Where(sq.And{
		sq.Eq{
			"p.paused": false,
			// A template's jobs structurally cannot build:
			// createAdmittedJobBuild refuses every build on a template pipeline
			// (ErrPipelineTemplateBuild), so its job rows can never carry a
			// completed build or a next_build_id and therefore ALWAYS satisfy
			// the idle subquery below. The damage is not cosmetic: a paused
			// template makes CreateRunInTx refuse with ErrPipelineRunPaused,
			// and buildLogCollector skips paused pipelines — run build logs are
			// reached only through the template's job rows
			// (pipeline.ChronoRunBuilds), so every run of that template stops
			// being reaped.
			"p.template": false,
			// Run payloads stay in scope, and this sweep is the only brake on
			// a run that never terminalises. A run whose expected job never
			// produces a build stays 'running' forever, so
			// pipelineRunReclaimLifecycle -- which requires a terminal status
			// -- never collects it, and checkFactory.Resources, which filters
			// on p.paused = false, keeps lidar checking its resources forever.
			// Nothing else can stop that. The pause is reversible from both
			// ends: pipeline.Unpause is permitted while the run is still
			// 'running', and reopenPipelineRun clears
			// pipelinePauserAttribution alongside 'run-completed' when a
			// manual trigger reopens a terminal run. Unlike a template pause
			// this costs no log reaping: buildLogCollector walks
			// pipelineFactory.AllPipelines, which excludes payloads outright.
		},
		// subquery returns a list of pipelines who jobs ran WITHIN the range.
		// These are the pipelines that SHOULD NOT be paused which we use to
		// build our list of pipelines that SHOULD be paused
		sq.Expr(`p.id NOT IN (SELECT j.pipeline_id FROM jobs j
							LEFT JOIN builds b ON j.latest_completed_build_id = b.id
							WHERE j.pipeline_id = p.id
								AND j.active = true
								AND (
									(b.end_time > CURRENT_DATE - ?::INTERVAL)
									--Don't pause pipelines with builds currently running
									OR j.next_build_id IS NOT NULL
								)
						)`,
			strconv.Itoa(daysSinceLastBuild)+" day"),
		// Covers edge case where pipelines that were just set could be paused automatically
		// Only pauses the pipeline if it was last updated more than ${daysSinceLastBuild} days ago
		sq.Expr(`p.last_updated < CURRENT_DATE - ?::INTERVAL`, strconv.Itoa(daysSinceLastBuild)+" day"),
	}).RunWith(p.conn).Query()

	if err != nil {
		return err
	}

	pipelines, err := scanPipelines(p.conn, p.lockFactory, rows)
	if err != nil {
		return err
	}

	for _, pipeline := range pipelines {
		loggingData := p.generateLoggingData(pipeline)
		err = pipeline.Pause(pipelinePauserAttribution)
		// Payloads are the one candidate kind that can vanish between the scan
		// and the pause: the run reclaimer deletes the pipelines row, and
		// lockPipelineRunForPayload then reports it gone. That is a race, not a
		// failure, and it must not abandon the rest of the sweep.
		if errors.Is(err, ErrPipelineRunPayloadGone) || errors.Is(err, ErrPipelineRunNotFound) {
			logger.Info("skipped-reclaimed-run-payload", loggingData)
			continue
		}
		if err != nil {
			logger.Error("failed-to-pause-pipeline", err, loggingData)
			return err
		}
		logger.Info("paused-pipeline", loggingData)
	}

	return nil
}

func (_ *pipelinePauser) generateLoggingData(pipeline Pipeline) lager.Data {
	loggingData := lager.Data{"pipeline": pipeline.Name(), "team": pipeline.TeamName()}
	if len(pipeline.InstanceVars()) > 0 {
		loggingData["instanceVars"] = pipeline.InstanceVars().String
	}

	return loggingData
}
