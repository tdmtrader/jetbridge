package runs

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
	"github.com/concourse/concourse/hangar/output"
)

// PrepareTask supplies only retained inputs to a stable Run task. Leases are
// minted later, after the worker has allocated the actual container handle.
// sealed reports a producer whose capture is already sealed: a re-driven build
// reached a task that exited successfully, and its result is read from Hangar,
// so it is neither admitted nor given a container again.
func (s *ExecutionStarter) PrepareTask(ctx context.Context, buildID int, plan atc.TaskPlan, spec runtime.ContainerSpec) (runtime.ContainerSpec, bool, error) {
	selected, err := s.loadRunTaskResults(ctx, buildID, plan.TaskID)
	if err != nil {
		return spec, false, err
	}
	task := selected.Task
	if spec.TeamID != selected.TeamID || spec.Type != db.ContainerTypeTask || !filepath.IsAbs(spec.Dir) || task.Name != plan.Name || task.ConfigPath != plan.ConfigPath || !slices.Equal(task.RunInputs, plan.RunInputs) || !reflect.DeepEqual(task.RunResult, plan.RunResult) || !maps.Equal(task.InputMapping, plan.InputMapping) {
		return spec, false, atc.ErrInvalidRunInputs
	}
	if len(task.RunInputs) > 0 && (task.Config == nil || plan.Config == nil || !reflect.DeepEqual(task.Config.Inputs, plan.Config.Inputs)) {
		return spec, false, atc.ErrInvalidRunInputs
	}
	bindings, err := matchRunTaskInputs(spec, selected, false)
	if err != nil {
		return spec, false, err
	}
	prepared := spec
	prepared.RunTaskID = task.TaskID
	prepared.Inputs = append([]runtime.Input(nil), spec.Inputs...)
	for index, binding := range bindings {
		ref := binding.Ref
		prepared.Inputs[index].HangarTree = &ref
	}

	if task.RunResult != nil {
		if s.Output == nil {
			return spec, false, atc.ErrRunResultsUnavailable
		}
		sealed, err := s.runTaskSealed(ctx, buildID, task.TaskID)
		if err != nil || sealed {
			return spec, sealed, err
		}
		prepared.ExecutionControl, err = s.Output.Prepare(ctx, buildID, plan, prepared)
		if err != nil {
			return spec, false, err
		}
	}
	return prepared, false, nil
}

// resultInputPoll is how often a task whose in-Run result input is still
// sealing looks at its capture again.
const resultInputPoll = time.Second

// loadRunTaskResults loads a Run task once every in-Run result input it reads
// is published. A capture still pending or publishing is waited for, one short
// transaction per look and no lock held between them; its own capture deadline
// bounds the wait. A capture that was discarded or failed, or that the build
// never started, is unavailable.
func (s *ExecutionStarter) loadRunTaskResults(ctx context.Context, buildID int, taskID string) (db.RunTaskBinding, error) {
	for {
		var selected db.RunTaskBinding
		err := s.transaction(ctx, func(tx db.Tx) error {
			var err error
			selected, err = db.LoadRunTask(ctx, tx, buildID, taskID)
			return err
		})
		if err != nil {
			return selected, err
		}
		waiting := false
		for _, route := range selected.Task.RunInputs {
			if _, bound := selected.Inputs[route.Name]; bound || route.Result == "" {
				continue
			}
			state := selected.Results[route.Name]
			if state != output.CapturePending && state != output.CapturePublishing {
				return selected, fmt.Errorf("%w: Run result %s was not published", atc.ErrRunInputUnavailable, route.Result)
			}
			waiting = true
		}
		if !waiting {
			return selected, nil
		}
		select {
		case <-ctx.Done():
			return selected, ctx.Err()
		case <-time.After(resultInputPoll):
		}
	}
}

// runTaskSealed reports whether a retained task's capture is already sealed.
func (s *ExecutionStarter) runTaskSealed(ctx context.Context, buildID int, taskID string) (bool, error) {
	var sealed bool
	err := s.transaction(ctx, func(tx db.Tx) error {
		capture, found, err := s.Factory.RunCaptureTask(ctx, tx, buildID, taskID)
		sealed = found && capture.Capture.State.Sealed()
		return err
	})
	return sealed, err
}

func runInputDestination(dir string, task atc.TaskStep, name string) (string, bool) {
	if task.Config == nil {
		return "", false
	}
	for _, input := range task.Config.Inputs {
		if input.Name == name {
			path := input.Path
			if path == "" {
				path = input.Name
			}
			return filepath.Join(dir, path), true
		}
	}
	return "", false
}

func matchRunTaskInputs(spec runtime.ContainerSpec, selected db.RunTaskBinding, resolved bool) (map[int]atc.RunInputBinding, error) {
	if spec.TeamID != selected.TeamID || (resolved && spec.RunTaskID != selected.Task.TaskID) {
		return nil, atc.ErrInvalidRunInputs
	}
	result := map[int]atc.RunInputBinding{}
	used := map[int]bool{}
	for _, route := range selected.Task.RunInputs {
		path, found := runInputDestination(spec.Dir, selected.Task, route.Input)
		if !found {
			return nil, atc.ErrInvalidRunInputs
		}
		matches := 0
		for i, input := range spec.Inputs {
			if input.DestinationPath != path {
				continue
			}
			matches++
			if input.RunInput != route.Name || input.Artifact != nil || input.HangarRead != nil || used[i] {
				return nil, atc.ErrInvalidRunInputs
			}
			binding, bound := selected.Inputs[route.Name]
			if !bound {
				return nil, atc.ErrRunInputUnavailable
			}
			if resolved {
				if input.HangarTree == nil || *input.HangarTree != binding.Ref {
					return nil, atc.ErrInvalidRunInputs
				}
			} else if input.HangarTree != nil {
				return nil, atc.ErrInvalidRunInputs
			}
			result[i] = binding
			used[i] = true
		}
		if matches != 1 {
			return nil, atc.ErrInvalidRunInputs
		}
	}
	for i, input := range spec.Inputs {
		if !used[i] && (input.RunInput != "" || input.HangarTree != nil || input.HangarRead != nil) {
			return nil, atc.ErrInvalidRunInputs
		}
	}
	return result, nil
}
