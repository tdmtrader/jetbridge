package present

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type PipelineOptions struct {
	AuthorizedForParams bool
	CanCreateRun        bool
}

func Pipeline(savedPipeline db.Pipeline, options PipelineOptions) atc.Pipeline {
	atcPipeline := atc.Pipeline{
		ID:            savedPipeline.ID(),
		Name:          savedPipeline.Name(),
		InstanceVars:  savedPipeline.InstanceVars(),
		TeamName:      savedPipeline.TeamName(),
		Paused:        savedPipeline.Paused(),
		PausedBy:      savedPipeline.PausedBy(),
		Public:        savedPipeline.Public(),
		Archived:      savedPipeline.Archived(),
		Groups:        savedPipeline.Groups(),
		Display:       savedPipeline.Display(),
		ParentBuildID: savedPipeline.ParentBuildID(),
		ParentJobID:   savedPipeline.ParentJobID(),
		LastUpdated:   savedPipeline.LastUpdated().Unix(),
	}

	if !savedPipeline.PausedAt().IsZero() {
		atcPipeline.PausedAt = savedPipeline.PausedAt().Unix()
	}

	if savedPipeline.Template() {
		template := true
		lastRunNumber := savedPipeline.LastRunNumber()
		canCreateRun := options.CanCreateRun
		atcPipeline.Template = &template
		atcPipeline.LastRunNumber = &lastRunNumber
		atcPipeline.CanCreateRun = &canCreateRun
		if options.AuthorizedForParams {
			paramsSchema := savedPipeline.Params()
			if paramsSchema == nil {
				paramsSchema = []atc.ParamSchema{}
			}
			atcPipeline.ParamsSchema = &paramsSchema
		}
		return atcPipeline
	}

	if _, isPayload := savedPipeline.PipelineRunID(); isPayload {
		template := false
		atcPipeline.Template = &template
		if runNumber, found := savedPipeline.RunNumber(); found {
			atcPipeline.RunNumber = &runNumber
		}
		if baseRef, found := savedPipeline.BasePipelineRef(); found {
			atcPipeline.RunTemplateRef = &atc.PipelineIdentifier{
				TeamName:     savedPipeline.TeamName(),
				PipelineName: baseRef.Name,
				InstanceVars: baseRef.InstanceVars,
			}
		}
	}

	return atcPipeline
}
