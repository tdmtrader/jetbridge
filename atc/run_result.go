package atc

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// The declaration can be saved and reviewed while the joint Run/cancellation/
// Hangar activation checkpoint is held. It must never fall back to a legacy Run.
var ErrRunResultPending = errors.New("Run result is not yet terminal")

var ErrRunResultsUnavailable = errors.New("Run result execution is not activated")

// RunResult selects one declared output of this stable template task. Keeping
// the declaration on the producer makes two selected outputs unrepresentable.
type RunResult struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

func (result *RunResult) UnmarshalJSON(data []byte) error {
	type wire RunResult
	var value wire
	if err := unmarshalStrict(data, &value); err != nil {
		return err
	}
	*result = RunResult(value)
	return nil
}

// RunTaskDeclaration is the stable identity retained with a Run definition.
// JobName identifies the containing job in the supplied config. TaskID remains
// the identity when materialization or a template edit changes display names.
type RunTaskDeclaration struct {
	TaskID  string     `json:"task_id"`
	JobName string     `json:"job"`
	Result  *RunResult `json:"result,omitempty"`
	Inputs  []RunInput `json:"inputs,omitempty"`
}

// RunTaskDeclarations validates the bounded mapping and derives it from the
// executable graph. Callers distinguish a template from its trusted materialized
// payload; ordinary pipeline admission must reject a nonempty answer.
func RunTaskDeclarations(config Config) ([]RunTaskDeclaration, error) {
	var declarations []RunTaskDeclaration
	seenTasks, seenResults := map[string]bool{}, map[string]bool{}
	seenInputs := map[string]bool{}
	inputEdges := 0
	expected := expectedJobNames(config.Jobs, entryJobNames(config.Jobs))
	for _, job := range config.Jobs {
		err := walkRunTasks(job.StepConfig(), false, func(task *TaskStep, repeated bool) error {
			if task.TaskID == "" {
				if task.RunResult != nil {
					return fmt.Errorf("task %q: run_result requires task_id", task.Name)
				}
				if len(task.RunInputs) > 0 {
					return fmt.Errorf("task %q: run_inputs requires task_id", task.Name)
				}
				return nil
			}
			id, err := uuid.Parse(task.TaskID)
			if err != nil || id == uuid.Nil || id.String() != task.TaskID {
				return fmt.Errorf("task_id %q must be a canonical UUID", task.TaskID)
			}
			if seenTasks[task.TaskID] {
				return fmt.Errorf("task_id %s is duplicated", task.TaskID)
			}
			seenTasks[task.TaskID] = true
			if len(seenTasks) > 256 {
				return fmt.Errorf("a template may declare at most 256 stable tasks")
			}
			if len(task.RunInputs) > 0 {
				inputEdges += len(task.RunInputs)
				if inputEdges > 256 {
					return fmt.Errorf("a template may declare at most 256 input routes")
				}
				if repeated {
					return fmt.Errorf("input target %s cannot occur in a repeated step", task.TaskID)
				}
				if task.Config == nil || task.ConfigPath != "" {
					return fmt.Errorf("input target %s requires an inline task config", task.TaskID)
				}
				if !expected[job.Name] {
					return fmt.Errorf("input target %s is outside expected work", task.TaskID)
				}
				if err := validateRunInputSlots(task); err != nil {
					return err
				}
				for _, input := range task.RunInputs {
					seenInputs[input.Name] = true
				}
				if len(seenInputs) > 64 {
					return fmt.Errorf("a template may declare at most 64 named inputs")
				}
			}
			if task.RunResult != nil {
				if repeated {
					return fmt.Errorf("result producer %s cannot occur in a repeated step", task.TaskID)
				}
				if task.Config == nil || task.ConfigPath != "" {
					return fmt.Errorf("result producer %s requires an inline task config", task.TaskID)
				}
				if err := output.OutputName(task.RunResult.Name).Validate(); err != nil {
					return fmt.Errorf("result name: %w", err)
				}
				if seenResults[task.RunResult.Name] {
					return fmt.Errorf("result name %q is duplicated", task.RunResult.Name)
				}
				seenResults[task.RunResult.Name] = true
				if len(seenResults) > 64 {
					return fmt.Errorf("a template may declare at most 64 results")
				}
				if !expected[job.Name] {
					return fmt.Errorf("result producer %s is outside expected work", task.TaskID)
				}
				matches := 0
				for _, declared := range task.Config.Outputs {
					if declared.Name == task.RunResult.Output {
						matches++
					}
				}
				if err := output.OutputName(task.RunResult.Output).Validate(); err != nil || matches != 1 {
					return fmt.Errorf("result %q must select exactly one declared output", task.RunResult.Name)
				}
			}
			declarations = append(declarations, RunTaskDeclaration{TaskID: task.TaskID, JobName: job.Name, Result: task.RunResult, Inputs: task.RunInputs})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	mapping, err := json.Marshal(declarations)
	if err != nil {
		return nil, err
	}
	if len(mapping) > 256*1024 {
		return nil, fmt.Errorf("Run task mapping exceeds 256 KiB")
	}
	return declarations, nil
}

// Wrappers retain the repetition flag for both their body and hooks. The
// ordinary StepRecursor intentionally has no context about enclosing steps.
func walkRunTasks(step StepConfig, repeated bool, visit func(*TaskStep, bool) error) error {
	if step == nil {
		return nil
	}
	var children []StepConfig
	switch typed := step.(type) {
	case *TaskStep:
		return visit(typed, repeated)
	case *DoStep:
		for _, child := range typed.Steps {
			children = append(children, child.Config)
		}
	case *InParallelStep:
		for _, child := range typed.Config.Steps {
			children = append(children, child.Config)
		}
	case *AcrossStep:
		repeated = true
	case *RetryStep:
		repeated = repeated || typed.Attempts > 1
	case *OnSuccessStep:
		children = append(children, typed.Hook.Config)
	case *OnFailureStep:
		children = append(children, typed.Hook.Config)
	case *OnAbortStep:
		children = append(children, typed.Hook.Config)
	case *OnErrorStep:
		children = append(children, typed.Hook.Config)
	case *EnsureStep:
		children = append(children, typed.Hook.Config)
	}
	if wrapper, ok := step.(StepWrapper); ok {
		children = append(children, wrapper.Unwrap())
	}
	for _, child := range children {
		if err := walkRunTasks(child, repeated, visit); err != nil {
			return err
		}
	}
	return nil
}

// ErrRunOutputPending preserves an admitted producer until its Run-owned finish disposition is durable.
var ErrRunOutputPending = errors.New("Run output disposition is pending")

// RunTaskImage returns the rootfs_uri the stable task taskID runs from, but
// only when nothing else can put a container in that task's Pod: no image
// artifact replaces it, no image_resource supplies one and no sidecar runs
// beside it. Otherwise, or for a task config does not declare, it is empty.
// A credential delivered into a Run result producer is delivered into exactly
// this image, so callers compare it against an operator pin and refuse "".
func RunTaskImage(config Config, taskID string) string {
	if taskID == "" {
		return ""
	}
	var image string
	found := false
	for _, job := range config.Jobs {
		_ = walkRunTasks(job.StepConfig(), false, func(task *TaskStep, _ bool) error {
			if found || task.TaskID != taskID {
				return nil
			}
			found = true
			if task.Config != nil && task.ConfigPath == "" && task.ImageArtifactName == "" &&
				task.Config.ImageResource == nil && len(task.Sidecars) == 0 {
				image = task.Config.RootfsURI
			}
			return nil
		})
		if found {
			break
		}
	}
	return image
}
