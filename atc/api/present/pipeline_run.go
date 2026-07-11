package present

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

func PipelineRun(run db.PipelineRun) atc.PipelineRun {
	presented := atc.PipelineRun{
		ID:        run.ID(),
		Number:    run.Number(),
		Status:    string(run.Status()),
		Params:    run.Params(),
		CreatedBy: run.CreatedBy(),
		CreatedAt: run.CreatedAt().Unix(),
		Archived:  run.Archived(),
	}
	if completedAt, ok := run.CompletedAt(); ok {
		presented.CompletedAt = completedAt.Unix()
	}
	return presented
}

func PipelineRuns(runs []db.PipelineRun) []atc.PipelineRun {
	presented := []atc.PipelineRun{}
	for _, run := range runs {
		presented = append(presented, PipelineRun(run))
	}
	return presented
}
