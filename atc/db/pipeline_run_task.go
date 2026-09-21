package db

import (
	"context"
	"database/sql"

	"github.com/concourse/concourse/atc"
)

// RunTaskBinding is a task and its named inputs from the immutable admitted
// definition. A caller's task ID locates evidence; it does not grant authority.
type RunTaskBinding struct {
	RunID  int
	TeamID int
	Task   atc.TaskStep
	Inputs map[string]atc.RunInputBinding
}

// LoadRunTask rechecks the Run/build start fence under the owning domain's lock
// prefix before returning any input authority. The caller owns the transaction.
func LoadRunTask(ctx context.Context, tx Tx, buildID int, taskID string, epoch int64) (RunTaskBinding, error) {
	var result RunTaskBinding
	var job string
	err := tx.QueryRowContext(ctx, `SELECT r.id,p.team_id,coalesce(b.run_job_name,'')
		FROM builds b JOIN pipeline_runs r ON r.id=b.pipeline_run_id
		JOIN pipelines p ON p.id=r.template_pipeline_id
		WHERE b.id=$1 AND r.run_contract_version='v2'`, buildID).Scan(&result.RunID, &result.TeamID, &job)
	if err == sql.ErrNoRows || taskID == "" {
		return result, atc.ErrRunResultsUnavailable
	}
	if err != nil {
		return result, err
	}
	if err := lockRunExecutionBuild(ctx, tx, result.RunID, buildID, epoch); err != nil {
		return result, err
	}
	definition, found, err := readRunDefinition(tx, result.RunID)
	if err != nil {
		return result, err
	}
	if !found {
		return result, atc.ErrRunResultsUnavailable
	}
	matches := 0
	for _, declared := range definition.Materialized.Jobs {
		if declared.Name != job {
			continue
		}
		if err := declared.StepConfig().Visit(atc.StepRecursor{OnTask: func(task *atc.TaskStep) error {
			if task.TaskID == taskID {
				matches++
				result.Task = *task
			}
			return nil
		}}); err != nil {
			return result, err
		}
	}
	if matches != 1 {
		return result, atc.ErrInvalidRunInputs
	}
	bindings, err := readRunInputs(ctx, tx, result.RunID)
	if err != nil {
		return result, err
	}
	result.Inputs = map[string]atc.RunInputBinding{}
	for _, route := range result.Task.RunInputs {
		binding, found := bindings[route.Name]
		if !found || binding.Epoch != epoch || binding.Ref.Validate() != nil || binding.ClaimID.Validate() != nil {
			return result, atc.ErrRunInputUnavailable
		}
		result.Inputs[route.Name] = binding
	}
	return result, nil
}
