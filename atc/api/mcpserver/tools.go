package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// RegisterTools registers all MCP tools with the server.
func RegisterTools(s *Server, teamFactory db.TeamFactory, buildFactory db.BuildFactory, externalURL string, version string) {
	registerListPipelines(s, teamFactory)
	registerGetPipeline(s, teamFactory)
	registerSetPipeline(s, teamFactory)
	registerPausePipeline(s, teamFactory)
	registerUnpausePipeline(s, teamFactory)
	registerListJobs(s, teamFactory)
	registerListBuilds(s, teamFactory)
	registerGetBuild(s, buildFactory)
	registerGetBuildLog(s, buildFactory)
	registerTriggerJob(s, teamFactory, externalURL)
	registerAbortBuild(s, buildFactory)
	registerListResources(s, teamFactory)
	registerListResourceVersions(s, teamFactory)
	registerCheckResource(s, teamFactory)
	registerGetJob(s, teamFactory)
	registerListTeams(s, teamFactory)
	registerGetBuildPlan(s, buildFactory)
	registerGetInfo(s, externalURL, version)
}

// --- helpers ---

func findTeam(teamFactory db.TeamFactory, teamName string) (db.Team, error) {
	team, found, err := teamFactory.FindTeam(teamName)
	if err != nil {
		return nil, fmt.Errorf("finding team %q: %w", teamName, err)
	}
	if !found {
		return nil, fmt.Errorf("team %q not found", teamName)
	}
	return team, nil
}

func findPipeline(team db.Team, pipelineName string) (db.Pipeline, error) {
	pipelines, err := team.Pipelines()
	if err != nil {
		return nil, fmt.Errorf("listing pipelines: %w", err)
	}
	for _, p := range pipelines {
		if p.Name() == pipelineName {
			return p, nil
		}
	}
	return nil, fmt.Errorf("pipeline %q not found in team %q", pipelineName, team.Name())
}

func findJob(pipeline db.Pipeline, jobName string) (db.Job, error) {
	job, found, err := pipeline.Job(jobName)
	if err != nil {
		return nil, fmt.Errorf("finding job: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("job %q not found in pipeline %q", jobName, pipeline.Name())
	}
	return job, nil
}

// --- common input/output types ---

type teamPipelineInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
}

type pipelineInfo struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Paused   bool   `json:"paused"`
	Public   bool   `json:"public"`
	Archived bool   `json:"archived"`
	TeamName string `json:"team_name"`
}

type buildInfo struct {
	ID              int    `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	PipelineName    string `json:"pipeline_name"`
	JobName         string `json:"job_name"`
	TeamName        string `json:"team_name"`
	StartTime       int64  `json:"start_time,omitempty"`
	EndTime         int64  `json:"end_time,omitempty"`
	DurationSeconds int64  `json:"duration_seconds"`
}

func toBuildInfoFromAPI(b db.BuildForAPI) buildInfo {
	var dur int64
	if b.StartTime().Unix() > 0 && b.EndTime().Unix() > 0 {
		dur = b.EndTime().Unix() - b.StartTime().Unix()
	}
	return buildInfo{
		ID:              b.ID(),
		Name:            b.Name(),
		Status:          string(b.Status()),
		PipelineName:    b.PipelineName(),
		JobName:         b.JobName(),
		TeamName:        b.TeamName(),
		StartTime:       b.StartTime().Unix(),
		EndTime:         b.EndTime().Unix(),
		DurationSeconds: dur,
	}
}

// --- list_pipelines ---

type listPipelinesInput struct {
	Team string `json:"team"`
}

func registerListPipelines(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("list_pipelines",
		"List all pipelines. Requires team name.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team"},
			"properties": map[string]any{
				"team": map[string]any{"type": "string", "description": "Team name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listPipelinesInput
			if len(args) > 0 {
				if err := json.Unmarshal(args, &input); err != nil {
					return nil, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipelines, err := team.Pipelines()
			if err != nil {
				return nil, fmt.Errorf("listing pipelines: %w", err)
			}
			result := make([]pipelineInfo, len(pipelines))
			for i, p := range pipelines {
				result[i] = pipelineInfo{
					ID:       p.ID(),
					Name:     p.Name(),
					Paused:   p.Paused(),
					Public:   p.Public(),
					Archived: p.Archived(),
					TeamName: p.TeamName(),
				}
			}
			return result, nil
		},
	)
}

// --- get_pipeline ---

type getPipelineInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
}

type getPipelineOutput struct {
	Config  atc.Config       `json:"config"`
	Version db.ConfigVersion `json:"version"`
}

func registerGetPipeline(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("get_pipeline",
		"Get a pipeline's configuration and config version.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input getPipelineInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			config, err := pipeline.Config()
			if err != nil {
				return nil, fmt.Errorf("getting pipeline config: %w", err)
			}
			return getPipelineOutput{
				Config:  config,
				Version: pipeline.ConfigVersion(),
			}, nil
		},
	)
}

// --- set_pipeline ---

type setPipelineInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Config   string `json:"config"`
}

type setPipelineOutput struct {
	Created bool `json:"created"`
}

func registerSetPipeline(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("set_pipeline",
		"Create or update a pipeline configuration. Config should be JSON-encoded atc.Config.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline", "config"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"config":   map[string]any{"type": "string", "description": "Pipeline configuration as JSON"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input setPipelineInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			var config atc.Config
			if err := json.Unmarshal([]byte(input.Config), &config); err != nil {
				return nil, fmt.Errorf("invalid pipeline config: %w", err)
			}
			// Try to get existing config version for update
			var configVersion db.ConfigVersion
			existing, pErr := findPipeline(team, input.Pipeline)
			if pErr == nil {
				configVersion = db.ConfigVersion(existing.ConfigVersion())
			}
			_, created, err := team.SavePipeline(
				atc.PipelineRef{Name: input.Pipeline},
				config,
				configVersion,
				false,
			)
			if err != nil {
				return nil, fmt.Errorf("saving pipeline: %w", err)
			}
			return setPipelineOutput{Created: created}, nil
		},
	)
}

// --- pause_pipeline ---

func registerPausePipeline(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("pause_pipeline",
		"Pause a pipeline, preventing new builds from being triggered.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input teamPipelineInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			if err := pipeline.Pause("mcp"); err != nil {
				return nil, fmt.Errorf("pausing pipeline: %w", err)
			}
			return map[string]bool{"success": true}, nil
		},
	)
}

// --- unpause_pipeline ---

func registerUnpausePipeline(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("unpause_pipeline",
		"Unpause a pipeline, allowing new builds to be triggered.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input teamPipelineInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			if err := pipeline.Unpause(); err != nil {
				return nil, fmt.Errorf("unpausing pipeline: %w", err)
			}
			return map[string]bool{"success": true}, nil
		},
	)
}

// --- list_jobs ---

type listJobsInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
}

type jobInfo struct {
	Name          string     `json:"name"`
	PipelineName  string     `json:"pipeline_name"`
	TeamName      string     `json:"team_name"`
	Paused        bool       `json:"paused,omitempty"`
	NextBuild     *buildInfo `json:"next_build,omitempty"`
	FinishedBuild *buildInfo `json:"finished_build,omitempty"`
}

func registerListJobs(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("list_jobs",
		"List all jobs in a pipeline with their current status.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listJobsInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			jobs, err := pipeline.Jobs()
			if err != nil {
				return nil, fmt.Errorf("listing jobs: %w", err)
			}
			result := make([]jobInfo, len(jobs))
			for i, j := range jobs {
				result[i] = jobInfo{
					Name:         j.Name(),
					PipelineName: pipeline.Name(),
					TeamName:     team.Name(),
					Paused:       j.Paused(),
				}
			}
			return result, nil
		},
	)
}

// --- list_builds ---

type listBuildsInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
	Limit    int    `json:"limit,omitempty"`
}

func registerListBuilds(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("list_builds",
		"List recent builds for a specific job in a pipeline.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline", "job"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"job":      map[string]any{"type": "string", "description": "Job name"},
				"limit":    map[string]any{"type": "integer", "description": "Max builds to return (default 10)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listBuildsInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			job, err := findJob(pipeline, input.Job)
			if err != nil {
				return nil, err
			}
			limit := input.Limit
			if limit <= 0 {
				limit = 10
			}
			builds, _, err := job.Builds(db.Page{Limit: limit})
			if err != nil {
				return nil, fmt.Errorf("listing builds: %w", err)
			}
			result := make([]buildInfo, len(builds))
			for i, b := range builds {
				result[i] = toBuildInfoFromAPI(b)
			}
			return result, nil
		},
	)
}

// --- get_build ---

type getBuildInput struct {
	BuildID int `json:"build_id"`
}

func registerGetBuild(s *Server, buildFactory db.BuildFactory) {
	s.AddTool("get_build",
		"Get details for a specific build by ID.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"build_id"},
			"properties": map[string]any{
				"build_id": map[string]any{"type": "integer", "description": "Build ID"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input getBuildInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			build, found, err := buildFactory.BuildForAPI(input.BuildID)
			if err != nil {
				return nil, fmt.Errorf("getting build: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("build %d not found", input.BuildID)
			}
			return toBuildInfoFromAPI(build), nil
		},
	)
}

// --- get_build_log ---

type getBuildLogInput struct {
	BuildID int `json:"build_id"`
}

type getBuildLogOutput struct {
	Log string `json:"log"`
}

func registerGetBuildLog(s *Server, buildFactory db.BuildFactory) {
	s.AddTool("get_build_log",
		"Get the log output for a build.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"build_id"},
			"properties": map[string]any{
				"build_id": map[string]any{"type": "integer", "description": "Build ID"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input getBuildLogInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			build, found, err := buildFactory.Build(input.BuildID)
			if err != nil {
				return nil, fmt.Errorf("getting build: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("build %d not found", input.BuildID)
			}
			events, err := build.Events(0)
			if err != nil {
				return nil, fmt.Errorf("getting build events: %w", err)
			}
			defer events.Close()

			var log string
			for {
				ev, err := events.Next()
				if err != nil {
					break
				}
				if ev.Event == "log" && ev.Data != nil {
					var logEvent struct {
						Payload string `json:"payload"`
					}
					if err := json.Unmarshal(*ev.Data, &logEvent); err == nil {
						log += logEvent.Payload
					}
				}
			}
			return getBuildLogOutput{Log: log}, nil
		},
	)
}

// --- trigger_job ---

type triggerJobInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
}

type triggerJobOutput struct {
	BuildID   int    `json:"build_id"`
	BuildName string `json:"build_name"`
	URL       string `json:"url"`
}

func registerTriggerJob(s *Server, teamFactory db.TeamFactory, externalURL string) {
	s.AddTool("trigger_job",
		"Trigger a new build of a job. Returns the build ID.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline", "job"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"job":      map[string]any{"type": "string", "description": "Job name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input triggerJobInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			job, err := findJob(pipeline, input.Job)
			if err != nil {
				return nil, err
			}
			build, err := job.CreateBuild("mcp")
			if err != nil {
				return nil, fmt.Errorf("triggering job: %w", err)
			}
			url := fmt.Sprintf("%s/teams/%s/pipelines/%s/jobs/%s/builds/%s",
				externalURL, input.Team, input.Pipeline, input.Job, build.Name())
			return triggerJobOutput{
				BuildID:   build.ID(),
				BuildName: build.Name(),
				URL:       url,
			}, nil
		},
	)
}

// --- abort_build ---

type abortBuildInput struct {
	BuildID int `json:"build_id"`
}

func registerAbortBuild(s *Server, buildFactory db.BuildFactory) {
	s.AddTool("abort_build",
		"Abort a running build by its ID.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"build_id"},
			"properties": map[string]any{
				"build_id": map[string]any{"type": "integer", "description": "Build ID to abort"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input abortBuildInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			build, found, err := buildFactory.Build(input.BuildID)
			if err != nil {
				return nil, fmt.Errorf("getting build: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("build %d not found", input.BuildID)
			}
			if err := build.MarkAsAborted(); err != nil {
				return nil, fmt.Errorf("aborting build: %w", err)
			}
			return map[string]bool{"success": true}, nil
		},
	)
}

// --- list_resources ---

type listResourcesInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
}

type resourceInfo struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	PipelineName string `json:"pipeline_name"`
	TeamName     string `json:"team_name"`
}

func registerListResources(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("list_resources",
		"List all resources in a pipeline.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listResourcesInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			resources, err := pipeline.Resources()
			if err != nil {
				return nil, fmt.Errorf("listing resources: %w", err)
			}
			result := make([]resourceInfo, len(resources))
			for i, r := range resources {
				result[i] = resourceInfo{
					Name:         r.Name(),
					Type:         r.Type(),
					PipelineName: pipeline.Name(),
					TeamName:     team.Name(),
				}
			}
			return result, nil
		},
	)
}

// --- list_resource_versions ---

type listResourceVersionsInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Resource string `json:"resource"`
	Limit    int    `json:"limit,omitempty"`
}

func registerListResourceVersions(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("list_resource_versions",
		"List versions of a resource in a pipeline.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline", "resource"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"resource": map[string]any{"type": "string", "description": "Resource name"},
				"limit":    map[string]any{"type": "integer", "description": "Max versions to return (default 10)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listResourceVersionsInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			resource, found, err := pipeline.Resource(input.Resource)
			if err != nil {
				return nil, fmt.Errorf("finding resource: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("resource %q not found", input.Resource)
			}
			limit := input.Limit
			if limit <= 0 {
				limit = 10
			}
			versions, _, _, err := resource.Versions(db.Page{Limit: limit}, atc.Version{})
			if err != nil {
				return nil, fmt.Errorf("listing resource versions: %w", err)
			}
			return versions, nil
		},
	)
}

// --- check_resource ---

type checkResourceInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Resource string `json:"resource"`
}

func registerCheckResource(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("check_resource",
		"Trigger a check for new versions of a resource.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline", "resource"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"resource": map[string]any{"type": "string", "description": "Resource name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input checkResourceInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			resource, found, err := pipeline.Resource(input.Resource)
			if err != nil {
				return nil, fmt.Errorf("finding resource: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("resource %q not found", input.Resource)
			}
			if err := resource.NotifyScan(); err != nil {
				return nil, fmt.Errorf("triggering resource check: %w", err)
			}
			return map[string]any{"success": true, "message": "resource check triggered"}, nil
		},
	)
}

// --- get_job ---

type getJobInput struct {
	Team     string `json:"team"`
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
}

type getJobOutput struct {
	Name         string   `json:"name"`
	PipelineName string   `json:"pipeline_name"`
	TeamName     string   `json:"team_name"`
	Paused       bool     `json:"paused"`
	Inputs       []string `json:"inputs"`
	Outputs      []string `json:"outputs"`
}

func registerGetJob(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("get_job",
		"Get detailed information about a job including its inputs and outputs.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"team", "pipeline", "job"},
			"properties": map[string]any{
				"team":     map[string]any{"type": "string", "description": "Team name"},
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"job":      map[string]any{"type": "string", "description": "Job name"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input getJobInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			team, err := findTeam(teamFactory, input.Team)
			if err != nil {
				return nil, err
			}
			pipeline, err := findPipeline(team, input.Pipeline)
			if err != nil {
				return nil, err
			}
			job, err := findJob(pipeline, input.Job)
			if err != nil {
				return nil, err
			}
			config, err := pipeline.Config()
			if err != nil {
				return nil, fmt.Errorf("getting pipeline config: %w", err)
			}

			// Find job inputs/outputs from pipeline config
			var inputs, outputs []string
			for _, jobConfig := range config.Jobs {
				if jobConfig.Name == job.Name() {
					for _, input := range jobConfig.Inputs() {
						inputs = append(inputs, input.Name)
					}
					for _, output := range jobConfig.Outputs() {
						outputs = append(outputs, output.Name)
					}
					break
				}
			}

			return getJobOutput{
				Name:         job.Name(),
				PipelineName: pipeline.Name(),
				TeamName:     team.Name(),
				Paused:       job.Paused(),
				Inputs:       inputs,
				Outputs:      outputs,
			}, nil
		},
	)
}

// --- list_teams ---

func registerListTeams(s *Server, teamFactory db.TeamFactory) {
	s.AddTool("list_teams",
		"List all teams.",
		MustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			teams, err := teamFactory.GetTeams()
			if err != nil {
				return nil, fmt.Errorf("listing teams: %w", err)
			}
			result := make([]map[string]any, len(teams))
			for i, t := range teams {
				result[i] = map[string]any{
					"id":   t.ID(),
					"name": t.Name(),
				}
			}
			return result, nil
		},
	)
}

// --- get_build_plan ---

type getBuildPlanInput struct {
	BuildID int `json:"build_id"`
}

func registerGetBuildPlan(s *Server, buildFactory db.BuildFactory) {
	s.AddTool("get_build_plan",
		"Get the execution plan for a build.",
		MustJSON(map[string]any{
			"type":     "object",
			"required": []string{"build_id"},
			"properties": map[string]any{
				"build_id": map[string]any{"type": "integer", "description": "Build ID"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input getBuildPlanInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			build, found, err := buildFactory.Build(input.BuildID)
			if err != nil {
				return nil, fmt.Errorf("getting build: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("build %d not found", input.BuildID)
			}
			publicPlan := build.PublicPlan()
			if publicPlan == nil {
				return map[string]string{"plan": "no plan available"}, nil
			}
			return json.RawMessage(*publicPlan), nil
		},
	)
}

// --- get_info ---

type getInfoOutput struct {
	Version string `json:"version"`
	URL     string `json:"external_url"`
}

func registerGetInfo(s *Server, externalURL string, version string) {
	s.AddTool("get_info",
		"Get Concourse server information.",
		MustJSON(map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			return getInfoOutput{
				Version: version,
				URL:     externalURL,
			}, nil
		},
	)
}

