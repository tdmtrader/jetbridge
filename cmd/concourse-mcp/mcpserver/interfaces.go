package mcpserver

import (
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/go-concourse/concourse"
)

// ClientAPI is the subset of concourse.Client used by the MCP server.
//
//counterfeiter:generate . ClientAPI
type ClientAPI interface {
	Build(buildID string) (atc.Build, bool, error)
	BuildEvents(buildID string) (concourse.Events, error)
	AbortBuild(buildID string) error
	ListPipelines() ([]atc.Pipeline, error)
	FindTeam(teamName string) (concourse.Team, error)
}

// TeamAPI is the subset of concourse.Team used by the MCP server.
//
//counterfeiter:generate . TeamAPI
type TeamAPI interface {
	Name() string
	ListPipelines() ([]atc.Pipeline, error)
	ListJobs(pipelineRef atc.PipelineRef) ([]atc.Job, error)
	PipelineConfig(pipelineRef atc.PipelineRef) (atc.Config, string, bool, error)
	CreateOrUpdatePipelineConfig(pipelineRef atc.PipelineRef, configVersion string, passedConfig []byte, checkCredentials bool) (bool, bool, []concourse.ConfigWarning, error)
	CreateJobBuild(pipelineRef atc.PipelineRef, jobName string) (atc.Build, error)
	JobBuilds(pipelineRef atc.PipelineRef, jobName string, page concourse.Page) ([]atc.Build, concourse.Pagination, bool, error)
	PausePipeline(pipelineRef atc.PipelineRef) (bool, error)
	UnpausePipeline(pipelineRef atc.PipelineRef) (bool, error)
}
