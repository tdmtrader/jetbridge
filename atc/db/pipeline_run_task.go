package db

import (
	"context"
	"database/sql"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RunTaskBinding is a task and its named inputs from the immutable admitted
// definition. A caller's task ID locates evidence; it does not grant authority.
//
// An in-Run result input is in Inputs only once its capture is published.
// Results holds the capture state of each in-Run result input by route name,
// "" when the build has no capture of that result.
type RunTaskBinding struct {
	RunID   int
	TeamID  int
	Task    atc.TaskStep
	Inputs  map[string]atc.RunInputBinding
	Results map[string]output.CaptureState
}

// LoadRunTask rechecks the Run/build start fence under the owning domain's lock
// prefix before returning any input authority. The caller owns the transaction.
func LoadRunTask(ctx context.Context, tx Tx, buildID int, taskID string) (RunTaskBinding, error) {
	var result RunTaskBinding
	var job string
	err := tx.QueryRowContext(ctx, `SELECT r.id,p.team_id,coalesce(b.run_job_name,'')
		FROM builds b JOIN pipeline_runs r ON r.id=b.pipeline_run_id
		JOIN pipelines p ON p.id=r.template_pipeline_id
		WHERE b.id=$1`, buildID).Scan(&result.RunID, &result.TeamID, &job)
	if err == sql.ErrNoRows || taskID == "" {
		return result, atc.ErrRunResultsUnavailable
	}
	if err != nil {
		return result, err
	}
	if err := lockRunExecutionBuild(ctx, tx, result.RunID, buildID); err != nil {
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
	result.Results = map[string]output.CaptureState{}
	for _, route := range result.Task.RunInputs {
		if route.Result != "" {
			state, binding, err := readRunResultInput(ctx, tx, result.RunID, buildID, route.Result)
			if err != nil {
				return result, err
			}
			result.Results[route.Name] = state
			if state == output.CapturePublished {
				result.Inputs[route.Name] = binding
			}
			continue
		}
		binding, found := bindings[route.Name]
		if !found || binding.Ref.Validate() != nil || binding.ClaimID.Validate() != nil {
			return result, atc.ErrRunInputUnavailable
		}
		result.Inputs[route.Name] = binding
	}
	return result, nil
}

// readRunResultInput reads this build's capture of one of its Run's results.
// Only a published capture binds: its tree ref under the claim its own
// publication holds, which stays live while the Run is running.
func readRunResultInput(ctx context.Context, tx Tx, runID, buildID int, name string) (output.CaptureState, atc.RunInputBinding, error) {
	var execution, outputName string
	err := tx.QueryRowContext(ctx, `SELECT execution_id, output_name FROM pipeline_run_captures
		WHERE run_id=$1 AND build_id=$2 AND result_name=$3`, runID, buildID, name).Scan(&execution, &outputName)
	if err == sql.ErrNoRows {
		return "", atc.RunInputBinding{}, nil
	}
	if err != nil {
		return "", atc.RunInputBinding{}, err
	}
	capture, err := runCaptureOutputRepository().GetCapture(ctx, tx, output.CaptureKey{
		ExecutionID: executioncontrol.ExecutionID(execution), Output: output.OutputName(outputName),
	})
	if err != nil || capture.State != output.CapturePublished {
		return capture.State, atc.RunInputBinding{}, err
	}
	ref, err := capture.Ref()
	if err != nil {
		return "", atc.RunInputBinding{}, err
	}
	return capture.State, atc.RunInputBinding{
		Source:  atc.RunInputSource{RunID: runID, Result: name},
		Ref:     ref,
		ClaimID: capture.Key.ClaimID(),
	}, nil
}
