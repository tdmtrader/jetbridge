package atc

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/concourse/concourse/atc/runinput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

var (
	ErrInvalidRunInputs    = errors.New("Run inputs do not match the retained task declarations")
	ErrRunInputUnavailable = errors.New("Run input source is unavailable or not authorized")
)

// RunInputSource names a prior result or a sealed source authenticated on first
// admission. Bearer is transient and must never enter retained invocation data.
type RunInputSource struct {
	RunID    int    `json:"run_id,omitempty"`
	Result   string `json:"result,omitempty"`
	SourceID string `json:"source_id,omitempty"`
	Bearer   string `json:"bearer,omitempty"`
}

func (source RunInputSource) Validate() error {
	if source.SourceID != "" {
		if source.RunID != 0 || source.Result != "" || !runinput.SourceIDValid(source.SourceID) || len(source.Bearer) > 8192 {
			return ErrInvalidRunInputs
		}
		return nil
	}
	if source.Bearer != "" || source.RunID <= 0 || output.OutputName(source.Result).Validate() != nil {
		return ErrInvalidRunInputs
	}
	return nil
}

func (source RunInputSource) WithoutBearer() RunInputSource {
	source.Bearer = ""
	return source
}

// Decode the closed wire shape here; admission validates source semantics after
// its replay lookup so a missing stable source ID is an idempotency conflict.
func (source *RunInputSource) UnmarshalJSON(data []byte) error {
	type wire RunInputSource
	var value wire
	if err := unmarshalStrict(data, &value); err != nil {
		return err
	}
	*source = RunInputSource(value)
	return nil
}

// RunInputBinding is retained admission evidence used by task input delivery.
// It is server-owned and is not accepted as an input source descriptor.
type RunInputBinding struct {
	Source  RunInputSource `json:"source"`
	Ref     hangar.TreeRef `json:"ref"`
	ClaimID output.ClaimID `json:"claim_id"`
	Epoch   int64          `json:"activation_epoch"`
}

// RunInput routes one named durable binding to an inline task's declared input.
// It contains no source ref or bearer; admission supplies the exact binding.
type RunInput struct {
	Name  string `json:"name"`
	Input string `json:"input"`
}

func (input *RunInput) UnmarshalJSON(data []byte) error {
	type wire RunInput
	var value wire
	if err := unmarshalStrict(data, &value); err != nil {
		return err
	}
	*input = RunInput(value)
	return nil
}

func validateRunInputSlots(task *TaskStep) error {
	seen := map[string]bool{}
	for _, route := range task.RunInputs {
		if err := output.OutputName(route.Name).Validate(); err != nil {
			return fmt.Errorf("input name: %w", err)
		}
		if seen[route.Input] {
			return fmt.Errorf("task %s input route %q is duplicated", task.TaskID, route.Input)
		}
		seen[route.Input] = true
		if _, ok := task.InputMapping[route.Input]; ok {
			return fmt.Errorf("Run input %q cannot also use input_mapping", route.Input)
		}
		matches := 0
		var selected TaskInputConfig
		for _, declared := range task.Config.Inputs {
			if declared.Name == route.Input {
				matches++
				selected = declared
			}
		}
		if err := output.OutputName(route.Input).Validate(); err != nil || matches != 1 {
			return fmt.Errorf("Run input %q must select exactly one declared input", route.Name)
		}
		selectedPath, err := runInputPath(selected.Path, selected.Name)
		if err != nil {
			return err
		}
		// An exact read-only input cannot share its mount with an ordinary input,
		// writable output, cache or scratch directory, including parent mounts.
		for _, other := range task.Config.Inputs {
			if other.Name == selected.Name {
				continue
			}
			if err := rejectInputOverlap(selectedPath, other.Path, other.Name); err != nil {
				return err
			}
		}
		for _, other := range task.Config.Outputs {
			if err := rejectInputOverlap(selectedPath, other.Path, other.Name); err != nil {
				return err
			}
		}
		for _, other := range task.Config.Caches {
			if err := rejectInputOverlap(selectedPath, other.Path, ""); err != nil {
				return err
			}
		}
		for _, other := range task.Config.ScratchPaths {
			if err := rejectInputOverlap(selectedPath, other.Path, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func runInputPath(value, fallback string) (string, error) {
	if value == "" {
		value = fallback
	}
	if strings.Contains(value, "((") || strings.Contains(value, "))") {
		return "", fmt.Errorf("Run input mount paths must be literal")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) || strings.ContainsAny(value, "\\\x00") {
		return "", fmt.Errorf("Run input mount paths must be contained relative paths")
	}
	return clean, nil
}

func rejectInputOverlap(selected, value, fallback string) error {
	other, err := runInputPath(value, fallback)
	if err != nil {
		return err
	}
	if selected == other || strings.HasPrefix(selected, other+"/") || strings.HasPrefix(other, selected+"/") {
		return fmt.Errorf("Run input mount %q overlaps another task mount", selected)
	}
	return nil
}
