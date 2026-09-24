package db

import (
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

// attemptRunCompletion wakes the Run result finalizer when one of a Run's
// builds settles. Completion itself -- status, time and result manifest in one
// terminal publication -- takes the activation and team prefix in a fresh
// transaction through FinalizeOutputRun, which this caller, already holding
// build or job locks, cannot. The finalizer polls even if this wake-up is
// lost, so this never completes a Run itself and always answers false.
func attemptRunCompletion(tx Tx, runID int) (bool, error) {
	if _, err := lockPipelineRun(tx, runID); err != nil {
		return false, err
	}
	if _, err := tx.Exec("SELECT pg_notify($1, '')", atc.ComponentRunResults); err != nil {
		return false, err
	}
	return false, nil
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

// The locked effective set is shared by legacy status and v2 result selection.
// Reruns preserve the original build's ordering key; only its latest rerun wins.
type effectiveRunBuild struct {
	ID     int
	Status BuildStatus
}
type runCompletionState struct {
	Status atc.RunStatus
	Builds map[string]effectiveRunBuild
}

func inspectRunCompletion(tx Tx, runID, payloadID int) (runCompletionState, bool, error) {
	var blocked bool
	err := tx.QueryRow(`
		SELECT EXISTS (
			SELECT 1
			FROM builds
			WHERE pipeline_run_id = $1
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
		return runCompletionState{}, false, err
	}
	if blocked {
		return runCompletionState{}, false, nil
	}

	rows, err := tx.Query(`
		SELECT DISTINCT ON (job_id) job_id, id, run_job_name, status
		FROM builds
		WHERE pipeline_run_id = $1
		  AND run_job_name IS NOT NULL
		  AND status IN ('succeeded', 'failed', 'errored', 'aborted')
		ORDER BY job_id, COALESCE(rerun_of, rerun_of_old, id) DESC, id DESC
	`, runID)
	if err != nil {
		return runCompletionState{}, false, err
	}
	defer Close(rows)

	latest := map[string]effectiveRunBuild{}
	status := atc.RunStatusSucceeded
	severity := 0
	for rows.Next() {
		var jobID, buildID int
		var jobName string
		var buildStatus BuildStatus
		if err = rows.Scan(&jobID, &buildID, &jobName, &buildStatus); err != nil {
			return runCompletionState{}, false, err
		}
		latest[jobName] = effectiveRunBuild{ID: buildID, Status: buildStatus}
		candidate, candidateSeverity := runStatusForBuild(buildStatus)
		if candidateSeverity > severity {
			status, severity = candidate, candidateSeverity
		}
	}
	if err = rows.Err(); err != nil {
		return runCompletionState{}, false, err
	}
	if len(latest) == 0 {
		return runCompletionState{}, false, nil
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
			return runCompletionState{}, false, err
		}
		if missingExpected {
			return runCompletionState{}, false, nil
		}
	}

	return runCompletionState{Status: status, Builds: latest}, true, nil
}
