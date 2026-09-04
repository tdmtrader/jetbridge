package db

import "errors"

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
