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
	ReopenTerminal  bool
	ObservedRunID   int
}

type jobBuildAdmission struct {
	jobID      int
	jobName    string
	policyKey  string
	pipelineID int
	teamID     int
	runID      int
	template   bool
}

// createJobBuild is the only insertion seam for non-check job builds. It
// resolves identity in the caller transaction and treats supplied run labels
// as untrusted input.
func createJobBuild(tx Tx, build *build, jobID int, args jobBuildArgs) (bool, error) {
	admission, err := lockJobBuildAdmission(tx, jobID, args.ObservedRunID, args.ReopenTerminal)
	if err != nil {
		return false, err
	}
	return createAdmittedJobBuild(tx, build, admission, args)
}

func createAdmittedJobBuild(tx Tx, build *build, admission jobBuildAdmission, args jobBuildArgs) (bool, error) {
	if admission.template {
		return false, ErrPipelineTemplateBuild
	}

	var err error
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

func lockJobBuildAdmission(tx Tx, jobID, hydratedRunID int, reopenTerminal bool) (jobBuildAdmission, error) {
	var observedRunID sql.NullInt64
	err := tx.QueryRow(`
		SELECT p.pipeline_run_id
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE j.id = $1
	`, jobID).Scan(&observedRunID)
	if err == sql.ErrNoRows && hydratedRunID != 0 {
		return jobBuildAdmission{}, ErrPipelineRunPayloadGone
	}
	if err != nil {
		return jobBuildAdmission{}, err
	}
	if hydratedRunID != 0 && (!observedRunID.Valid || int(observedRunID.Int64) != hydratedRunID) {
		return jobBuildAdmission{}, ErrPipelineRunPayloadGone
	}

	var lockedRun PipelineRun
	if observedRunID.Valid {
		lockedRun, err = lockPipelineRun(tx, int(observedRunID.Int64))
		if err != nil {
			return jobBuildAdmission{}, err
		}
		if _, found := lockedRun.InstancePipelineID(); !found {
			return jobBuildAdmission{}, ErrPipelineRunPayloadGone
		}
	}

	// The pipelines row lock is only taken for the run case. Three things the
	// unconditional `FOR UPDATE OF j, p` was protecting, and where they now live:
	//  1. a TOCTOU re-read of p.pipeline_run_id -- unnecessary: the
	//     pipeline_run_ownership_immutable trigger installed by
	//     migrations/1773105505_add_pipeline_template_runs.up.sql rejects any
	//     UPDATE that changes pipelines.pipeline_run_id, so the unlocked read
	//     above is authoritative for the row's whole lifetime;
	//  2. existence of the jobs/pipelines rows at insert time -- provided by the
	//     builds.job_id / builds.pipeline_id foreign keys, which take FOR KEY
	//     SHARE on the parent rows during the INSERT;
	//  3. serializing createAdmittedJobBuild's OnlyIfNoPending check-then-insert
	//     -- held by `FOR UPDATE OF j`, which stays unconditional because on this
	//     branch the EXISTS check runs before the build_number_seq UPDATE.
	// The run case additionally pins the payload pipeline row so it cannot be
	// reclaimed out from under a build that is attaching to the run.
	lockClause := "FOR UPDATE OF j"
	if observedRunID.Valid {
		lockClause = "FOR UPDATE OF j, p"
	}

	var admission jobBuildAdmission
	var liveRunID sql.NullInt64
	var policyKey sql.NullString
	err = tx.QueryRow(`
		SELECT j.id, j.name, j.run_policy_key, p.id, p.team_id, p.pipeline_run_id, p.template
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE j.id = $1
		`+lockClause, jobID).Scan(
		&admission.jobID,
		&admission.jobName,
		&policyKey,
		&admission.pipelineID,
		&admission.teamID,
		&liveRunID,
		&admission.template,
	)
	if err == sql.ErrNoRows && (lockedRun != nil || hydratedRunID != 0) {
		return jobBuildAdmission{}, ErrPipelineRunPayloadGone
	}
	if err != nil {
		return jobBuildAdmission{}, err
	}
	admission.policyKey = policyKey.String

	if !liveRunID.Valid {
		if lockedRun != nil {
			return jobBuildAdmission{}, ErrPipelineRunPayloadGone
		}
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
	if lockedRun.Status() != atc.RunStatusRunning {
		if !reopenTerminal {
			return jobBuildAdmission{}, ErrPipelineRunNotRunning
		}
		if err = reopenPipelineRun(tx, admission.runID, admission.pipelineID); err != nil {
			return jobBuildAdmission{}, err
		}
	}
	return admission, nil
}

func lockPipelineRunForPayload(tx Tx, pipelineID, hydratedRunID int) (PipelineRun, bool, error) {
	var observedRunID sql.NullInt64
	err := tx.QueryRow("SELECT pipeline_run_id FROM pipelines WHERE id = $1", pipelineID).Scan(&observedRunID)
	if err == sql.ErrNoRows && hydratedRunID != 0 {
		return nil, false, ErrPipelineRunPayloadGone
	}
	if err != nil {
		return nil, false, err
	}
	if hydratedRunID != 0 && (!observedRunID.Valid || int(observedRunID.Int64) != hydratedRunID) {
		return nil, false, ErrPipelineRunPayloadGone
	}
	if !observedRunID.Valid {
		return nil, false, nil
	}
	run, err := lockPipelineRun(tx, int(observedRunID.Int64))
	if errors.Is(err, ErrPipelineRunNotFound) && hydratedRunID != 0 {
		return nil, false, ErrPipelineRunPayloadGone
	}
	if err != nil {
		return nil, false, err
	}
	payloadID, found := run.InstancePipelineID()
	if !found || payloadID != pipelineID {
		return nil, false, ErrPipelineRunPayloadGone
	}
	return run, true, nil
}
