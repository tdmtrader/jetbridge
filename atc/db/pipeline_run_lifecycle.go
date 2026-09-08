package db

import (
	"database/sql"

	"github.com/concourse/concourse/atc"
)

// announceRunCompletion wakes everything that cares that a run just reached a
// terminal status. Every caller of attemptRunCompletion routes its true result
// here so the set of channels stays in one place.
//
// Both notifications are best-effort wake-ups after the completing
// transaction commits, so both are fire-and-forget. The bus coalesces
// notifications per channel and NotifySignal drops a signal when a listener's
// buffer is already full, which means a listener can miss one entirely.
// Nothing here may be treated as delivery: every consumer -- the reclaimer on
// its interval, and any future walker on
// atc.PipelineRunCompletedChannel -- must still poll, and must be correct
// when no notification ever arrives.
func announceRunCompletion(bus NotificationsBus) {
	bus.Notify(atc.ComponentReclaimerPipelineRuns)
	bus.Notify(atc.PipelineRunCompletedChannel)
}

func attemptRunCompletion(tx Tx, runID int) (bool, error) {
	run, err := lockPipelineRun(tx, runID)
	if err != nil {
		return false, err
	}
	if run.Status() != atc.RunStatusRunning {
		return false, nil
	}

	var payloadCount, payloadID int
	err = tx.QueryRow(`
		SELECT count(*), COALESCE(min(id), 0)
		FROM pipelines
		WHERE pipeline_run_id = $1
	`, runID).Scan(&payloadCount, &payloadID)
	if err != nil {
		return false, err
	}
	if payloadCount != 1 {
		return false, nil
	}

	var blocked bool
	err = tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM builds
			WHERE pipeline_run_id = $1
			  AND run_job_name IS NOT NULL
			  AND status IN ('pending', 'started')
		) OR EXISTS (
			SELECT 1
			FROM jobs j
			JOIN pipelines p ON p.id = j.pipeline_id
			WHERE p.id = $2
			  AND p.paused = false
			  AND j.active = true
			  AND j.paused = false
			  AND j.schedule_requested > j.last_scheduled
		)
	`, runID, payloadID).Scan(&blocked)
	if err != nil {
		return false, err
	}
	if blocked {
		return false, nil
	}

	rows, err := tx.Query(`
		SELECT DISTINCT ON (job_id) job_id, status
		FROM builds
		WHERE pipeline_run_id = $1
		  AND run_job_name IS NOT NULL
		  AND status IN ('succeeded', 'failed', 'errored', 'aborted')
		ORDER BY job_id, COALESCE(rerun_of, rerun_of_old, id) DESC, id DESC
	`, runID)
	if err != nil {
		return false, err
	}
	defer Close(rows)

	latest := map[int]BuildStatus{}
	status := atc.RunStatusSucceeded
	severity := 0
	for rows.Next() {
		var jobID int
		var buildStatus BuildStatus
		if err = rows.Scan(&jobID, &buildStatus); err != nil {
			return false, err
		}
		latest[jobID] = buildStatus
		candidate, candidateSeverity := runStatusForBuild(buildStatus)
		if candidateSeverity > severity {
			status, severity = candidate, candidateSeverity
		}
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	if len(latest) == 0 {
		return false, nil
	}

	if status == atc.RunStatusSucceeded {
		var missingExpected bool
		err = tx.QueryRow(`
			SELECT EXISTS (
				SELECT 1
				FROM jobs j
				WHERE j.pipeline_id = $1
				  AND j.active = true
				  AND j.run_expected = true
				  AND NOT EXISTS (
					SELECT 1
					FROM builds b
					WHERE b.job_id = j.id
					  AND b.pipeline_run_id = $2
					  AND b.run_job_name IS NOT NULL
					  AND b.status IN ('succeeded', 'failed', 'errored', 'aborted')
				  )
			)
		`, payloadID, runID).Scan(&missingExpected)
		if err != nil {
			return false, err
		}
		if missingExpected {
			return false, nil
		}
	}

	result, err := tx.Exec(`
		UPDATE pipeline_runs
		SET status = $2, completed_at = now()
		WHERE id = $1 AND status = 'running'
	`, runID, status)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if updated != 1 {
		return false, nil
	}

	_, err = tx.Exec(`
		UPDATE pipelines
		SET paused = true, paused_at = now(), paused_by = 'run-completed'
		WHERE id = $1 AND paused = false
	`, payloadID)
	if err != nil {
		return false, err
	}
	return true, nil
}

func runStatusForBuild(status BuildStatus) (atc.RunStatus, int) {
	switch status {
	case BuildStatusErrored:
		return atc.RunStatusErrored, 4
	case BuildStatusAborted:
		return atc.RunStatusAborted, 3
	case BuildStatusFailed:
		return atc.RunStatusFailed, 2
	default:
		return atc.RunStatusSucceeded, 1
	}
}

func reopenPipelineRun(tx Tx, runID, payloadID int) error {
	result, err := tx.Exec(`
		UPDATE pipeline_runs
		SET status = 'running', completed_at = NULL
		WHERE id = $1 AND status <> 'running'
	`, runID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return sql.ErrNoRows
	}

	_, err = tx.Exec(`
		UPDATE jobs
		SET last_scheduled = GREATEST(last_scheduled, schedule_requested)
		WHERE pipeline_id = $1
	`, payloadID)
	if err != nil {
		return err
	}
	// A payload pause is reversible only through the attribution it carries.
	// Two writers pause a payload without an operator asking: completion above
	// ('run-completed') and the idle sweep in pipeline_pauser.go. Both are
	// platform attributions, so both must dissolve when a manual trigger
	// reopens the run -- otherwise the sweep's pause survives reopen, the
	// admitted build never schedules, and the run is wedged with no way out.
	// A user pause is deliberate and must survive; it is not listed here.
	_, err = tx.Exec(`
		UPDATE pipelines
		SET paused = false, paused_at = NULL, paused_by = NULL
		WHERE id = $1 AND paused = true
		  AND (paused_by = 'run-completed' OR paused_by = $2)
	`, payloadID, pipelinePauserAttribution)
	return err
}
