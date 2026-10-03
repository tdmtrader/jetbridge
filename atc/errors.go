package atc

import (
	"fmt"
)

type MalformedConfigError struct {
	UnmarshalError error
}

type MalformedStepError struct {
	StepType string
	Err      error
}

func (err MalformedStepError) Error() string {
	return fmt.Sprintf("malformed %s step: %s", err.StepType, err.Err)
}

func (err MalformedStepError) Unwrap() error {
	return err.Err
}
