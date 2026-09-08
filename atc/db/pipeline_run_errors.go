package db

import (
	"errors"
	"fmt"

	"github.com/concourse/concourse/atc"
)

var (
	ErrPipelineRunNotTemplate              = errors.New("pipeline is not a template")
	ErrPipelineRunInstanced                = errors.New("template pipeline cannot have instance vars")
	ErrPipelineRunPaused                   = errors.New("template pipeline is paused")
	ErrPipelineRunArchived                 = errors.New("template pipeline is archived")
	ErrPipelineTemplateHasRuns             = errors.New("template with durable runs cannot stop being a template")
	ErrPipelineRunPayloadMutation          = errors.New("pipeline run payload cannot be mutated directly")
	ErrPipelineTemplateHasRunHistory       = errors.New("template with durable run history cannot be destroyed")
	ErrPipelineTemplateBuild               = errors.New("pipeline templates cannot create builds directly")
	ErrPipelineTemplateCheck               = errors.New("pipeline templates cannot be checked directly")
	ErrPipelineTemplateHasOrdinaryJobState = errors.New("pipeline with ordinary job history or task caches cannot become a template")
)

// ErrPipelineTemplateInvalid reports a stored template config that no longer
// satisfies template validation at run time -- a row written before save-time
// validation existed, or edited around it. It is not the caller's mistake, so
// it is a conflict rather than a bad request, but the reason has to reach the
// client all the same: without it the create route answers a bare 500.
type ErrPipelineTemplateInvalid struct{ Err error }

func (e ErrPipelineTemplateInvalid) Error() string {
	return "invalid pipeline template: " + e.Err.Error()
}

func (e ErrPipelineTemplateInvalid) Unwrap() error { return e.Err }

// ErrPipelineRunTerminal reports a build refused admission into a run that has
// already completed. A run is one execution of a template: it carries a number,
// the parameters it was created with, and the outputs those produced, and once
// it has settled that record is closed. Running the work again is a NEW run of
// the same template -- its own number, its own outputs -- not a second pass
// under an existing number, which would leave the run's status, its completion
// time and its build list describing two different executions at once.
//
// It carries the number and the settled status because the refusal is answered
// to a person who asked for a build by job name and has no other way to learn
// which run their job belonged to.
type ErrPipelineRunTerminal struct {
	Number int
	Status atc.RunStatus
}

func (e ErrPipelineRunTerminal) Error() string {
	return fmt.Sprintf("run #%d is complete (%s); run the template again", e.Number, e.Status)
}
