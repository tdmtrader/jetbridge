package present

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

type PipelinesOptions struct {
	OptionsForPipeline func(db.Pipeline) PipelineOptions
}

func Pipelines(savedPipelines []db.Pipeline, options PipelinesOptions) []atc.Pipeline {
	pipelines := make([]atc.Pipeline, len(savedPipelines))

	for i := range savedPipelines {
		pipelineOptions := PipelineOptions{}
		if options.OptionsForPipeline != nil {
			pipelineOptions = options.OptionsForPipeline(savedPipelines[i])
		}
		pipelines[i] = Pipeline(savedPipelines[i], pipelineOptions)
	}

	return pipelines
}
