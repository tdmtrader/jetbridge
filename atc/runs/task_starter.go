package runs

import (
	"context"
	"maps"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runtime"
)

// PrepareTask supplies only retained inputs to a stable Run task. Leases are
// minted later, after the worker has allocated the actual container handle.
func (s *ExecutionStarter) PrepareTask(ctx context.Context, buildID int, plan atc.TaskPlan, spec runtime.ContainerSpec) (runtime.ContainerSpec, error) {
	var selected db.RunTaskBinding
	err := s.transaction(ctx, func(tx db.Tx) error {
		var err error
		selected, err = db.LoadRunTask(ctx, tx, buildID, plan.TaskID, int64(s.Epoch))
		return err
	})
	if err != nil {
		return spec, err
	}
	task := selected.Task
	if spec.TeamID != selected.TeamID || spec.Type != db.ContainerTypeTask || !filepath.IsAbs(spec.Dir) || task.Name != plan.Name || task.ConfigPath != plan.ConfigPath || !slices.Equal(task.RunInputs, plan.RunInputs) || !reflect.DeepEqual(task.RunResult, plan.RunResult) || !maps.Equal(task.InputMapping, plan.InputMapping) {
		return spec, atc.ErrInvalidRunInputs
	}
	if len(task.RunInputs) > 0 && (task.Config == nil || plan.Config == nil || !reflect.DeepEqual(task.Config.Inputs, plan.Config.Inputs)) {
		return spec, atc.ErrInvalidRunInputs
	}
	bindings, err := matchRunTaskInputs(spec, selected, false)
	if err != nil {
		return spec, err
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
			return spec, atc.ErrRunResultsUnavailable
		}
		prepared.ExecutionControl, err = s.Output.Prepare(ctx, buildID, plan, prepared)
		if err != nil {
			return spec, err
		}
	}
	return prepared, nil
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
			binding := selected.Inputs[route.Name]
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
