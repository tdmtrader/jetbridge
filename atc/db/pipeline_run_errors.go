package db

import "errors"

var (
	ErrPipelineRunNotTemplate  = errors.New("pipeline is not a template")
	ErrPipelineRunInstanced    = errors.New("template pipeline cannot have instance vars")
	ErrPipelineRunPaused       = errors.New("template pipeline is paused")
	ErrPipelineRunArchived     = errors.New("template pipeline is archived")
	ErrPipelineTemplateHasRuns = errors.New("template with durable runs cannot stop being a template")
)
