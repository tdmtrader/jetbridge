package db

import (
	"database/sql"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

var (
	ErrPipelineRunNotFound    = errors.New("pipeline run not found")
	ErrPipelineRunNotRunning  = errors.New("pipeline run is not running")
	ErrPipelineRunPayloadGone = errors.New("pipeline run payload is missing")
	ErrPipelineRunOneOffBuild = errors.New("pipeline run payload cannot create one-off builds")
)

// lockPipelineRun is the shared durable-header serialization seam. Callers
// that also need a template lock must acquire that lock first.
func lockPipelineRun(tx Tx, runID int) (PipelineRun, error) {
	run := &pipelineRun{}
	err := scanPipelineRun(run, pipelineRunsQuery.
		Where(sq.Eq{"r.id": runID}).
		Suffix("FOR UPDATE OF r").
		RunWith(tx).
		QueryRow())
	if err == sql.ErrNoRows {
		return nil, ErrPipelineRunNotFound
	}
	if err != nil {
		return nil, err
	}
	return run, nil
}

type jobBuildArgs struct {
	Values          map[string]any
	NextBuildName   bool
	OnlyIfNoPending bool
}

type jobBuildAdmission struct {
	jobID      int
	jobName    string
	policyKey  string
	pipelineID int
	teamID     int
	runID      int
}

// createJobBuild is the only insertion seam for non-check job builds. It
// resolves identity in the caller transaction and treats supplied run labels
// as untrusted input.
func createJobBuild(tx Tx, build *build, jobID int, args jobBuildArgs) (bool, error) {
	admission, err := lockJobBuildAdmission(tx, jobID)
	if err != nil {
		return false, err
	}

	if args.OnlyIfNoPending {
		var exists bool
		err = tx.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM builds WHERE job_id = $1 AND status = 'pending'
		)`, admission.jobID).Scan(&exists)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}

	values := make(map[string]any, len(args.Values)+7)
	for key, value := range args.Values {
		values[key] = value
	}
	delete(values, "pipeline_run_id")
	delete(values, "run_job_name")
	delete(values, "run_job_key")
	values["job_id"] = admission.jobID
	values["pipeline_id"] = admission.pipelineID
	values["team_id"] = admission.teamID
	if admission.runID != 0 {
		values["pipeline_run_id"] = admission.runID
		values["run_job_name"] = admission.jobName
		values["run_job_key"] = admission.policyKey
	}

	if args.NextBuildName {
		var name string
		err = psql.Update("jobs").
			Set("build_number_seq", sq.Expr("build_number_seq + 1")).
			Where(sq.Eq{"id": admission.jobID}).
			Suffix("RETURNING build_number_seq").
			RunWith(tx).
			QueryRow().
			Scan(&name)
		if err != nil {
			return false, err
		}
		values["name"] = name
	}

	if err = createBuild(tx, build, values); err != nil {
		return false, err
	}
	return true, nil
}

func lockJobBuildAdmission(tx Tx, jobID int) (jobBuildAdmission, error) {
	var observedRunID sql.NullInt64
	err := tx.QueryRow(`
		SELECT p.pipeline_run_id
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE j.id = $1
	`, jobID).Scan(&observedRunID)
	if err != nil {
		return jobBuildAdmission{}, err
	}

	var lockedRun PipelineRun
	if observedRunID.Valid {
		lockedRun, err = lockPipelineRun(tx, int(observedRunID.Int64))
		if err != nil {
			return jobBuildAdmission{}, err
		}
		if lockedRun.Status() != atc.RunStatusRunning {
			return jobBuildAdmission{}, ErrPipelineRunNotRunning
		}
	}

	var admission jobBuildAdmission
	var liveRunID sql.NullInt64
	var policyKey sql.NullString
	err = tx.QueryRow(`
		SELECT j.id, j.name, j.run_policy_key, p.id, p.team_id, p.pipeline_run_id
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE j.id = $1
		FOR UPDATE OF j, p
	`, jobID).Scan(
		&admission.jobID,
		&admission.jobName,
		&policyKey,
		&admission.pipelineID,
		&admission.teamID,
		&liveRunID,
	)
	if err != nil {
		return jobBuildAdmission{}, err
	}
	admission.policyKey = policyKey.String

	if !liveRunID.Valid {
		return admission, nil
	}
	admission.runID = int(liveRunID.Int64)
	if lockedRun == nil || lockedRun.ID() != admission.runID {
		return jobBuildAdmission{}, ErrPipelineRunPayloadGone
	}
	payloadID, found := lockedRun.InstancePipelineID()
	if !found || payloadID != admission.pipelineID {
		return jobBuildAdmission{}, ErrPipelineRunPayloadGone
	}
	return admission, nil
}

func lockPipelineRunForPayload(tx Tx, pipelineID int) (PipelineRun, bool, error) {
	var observedRunID sql.NullInt64
	err := tx.QueryRow("SELECT pipeline_run_id FROM pipelines WHERE id = $1", pipelineID).Scan(&observedRunID)
	if err != nil {
		return nil, false, err
	}
	if !observedRunID.Valid {
		return nil, false, nil
	}
	run, err := lockPipelineRun(tx, int(observedRunID.Int64))
	if err != nil {
		return nil, false, err
	}
	payloadID, found := run.InstancePipelineID()
	if !found || payloadID != pipelineID {
		return nil, false, ErrPipelineRunPayloadGone
	}
	return run, true, nil
}

// consumeScheduleRequestCompletion is the Task 6 hook. Task 5 deliberately
// leaves lifecycle policy out of admission and scheduling debt consumption.
func consumeScheduleRequestCompletion(Tx, int) error { return nil }
