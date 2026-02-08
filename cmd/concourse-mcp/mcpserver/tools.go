package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/go-concourse/concourse"
)

func registerTools(s *Server, client ClientAPI, team TeamAPI, apiURL string) {
	registerListPipelines(s, client, team)
	registerGetPipeline(s, team)
	registerListJobs(s, client, team)
	registerListBuilds(s, team)
	registerGetBuild(s, client)
	registerGetBuildLog(s, client)
	registerTriggerJob(s, client, team, apiURL)
	registerSetPipeline(s, team)
	registerAbortBuild(s, client)
	registerPausePipeline(s, client, team)
	registerUnpausePipeline(s, client, team)
}

// --- list_pipelines ---

type listPipelinesInput struct {
	Team string `json:"team,omitempty"`
}

type pipelineInfo struct {
	Name     string `json:"name"`
	Paused   bool   `json:"paused"`
	Public   bool   `json:"public"`
	Archived bool   `json:"archived"`
	TeamName string `json:"team_name"`
}

func registerListPipelines(s *Server, client ClientAPI, defaultTeam TeamAPI) {
	s.addTool("list_pipelines",
		"List all pipelines. Optionally filter by team name.",
		mustJSON(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"team": map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listPipelinesInput
			if len(args) > 0 {
				if err := json.Unmarshal(args, &input); err != nil {
					return nil, fmt.Errorf("invalid arguments: %w", err)
				}
			}
			t, err := resolveTeam(client, defaultTeam, input.Team)
			if err != nil {
				return nil, err
			}
			pipelines, err := t.ListPipelines()
			if err != nil {
				return nil, fmt.Errorf("listing pipelines: %w", err)
			}
			result := make([]pipelineInfo, len(pipelines))
			for i, p := range pipelines {
				result[i] = pipelineInfo{
					Name:     p.Name,
					Paused:   p.Paused,
					Public:   p.Public,
					Archived: p.Archived,
					TeamName: p.TeamName,
				}
			}
			return result, nil
		},
	)
}

// --- get_pipeline ---

type getPipelineInput struct {
	Pipeline string `json:"pipeline"`
	Team     string `json:"team,omitempty"`
}

type getPipelineOutput struct {
	YAML    string `json:"yaml"`
	Version string `json:"version"`
}

func registerGetPipeline(s *Server, defaultTeam TeamAPI) {
	s.addTool("get_pipeline",
		"Get a pipeline's YAML configuration and config version.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input getPipelineInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			config, version, found, err := defaultTeam.PipelineConfig(atc.PipelineRef{Name: input.Pipeline})
			if err != nil {
				return nil, fmt.Errorf("getting pipeline config: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("pipeline %q not found", input.Pipeline)
			}
			yamlBytes, err := json.Marshal(config)
			if err != nil {
				return nil, fmt.Errorf("marshaling config: %w", err)
			}
			return getPipelineOutput{YAML: string(yamlBytes), Version: version}, nil
		},
	)
}

// --- list_jobs ---

type listJobsInput struct {
	Pipeline string `json:"pipeline"`
	Team     string `json:"team,omitempty"`
}

type jobInfo struct {
	Name         string     `json:"name"`
	PipelineName string     `json:"pipeline_name"`
	TeamName     string     `json:"team_name"`
	Paused       bool       `json:"paused,omitempty"`
	NextBuild    *buildInfo `json:"next_build,omitempty"`
	FinishedBuild *buildInfo `json:"finished_build,omitempty"`
}

func registerListJobs(s *Server, client ClientAPI, defaultTeam TeamAPI) {
	s.addTool("list_jobs",
		"List all jobs in a pipeline with their current status.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listJobsInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			t, err := resolveTeam(client, defaultTeam, input.Team)
			if err != nil {
				return nil, err
			}
			jobs, err := t.ListJobs(atc.PipelineRef{Name: input.Pipeline})
			if err != nil {
				return nil, fmt.Errorf("listing jobs: %w", err)
			}
			result := make([]jobInfo, len(jobs))
			for i, j := range jobs {
				info := jobInfo{
					Name:         j.Name,
					PipelineName: j.PipelineName,
					TeamName:     j.TeamName,
					Paused:       j.Paused,
				}
				if j.NextBuild != nil {
					bi := toBuildInfo(*j.NextBuild)
					info.NextBuild = &bi
				}
				if j.FinishedBuild != nil {
					bi := toBuildInfo(*j.FinishedBuild)
					info.FinishedBuild = &bi
				}
				result[i] = info
			}
			return result, nil
		},
	)
}

// --- list_builds ---

type listBuildsInput struct {
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
	Team     string `json:"team,omitempty"`
	Limit    int    `json:"limit,omitempty"`
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

func registerListBuilds(s *Server, defaultTeam TeamAPI) {
	s.addTool("list_builds",
		"List recent builds for a specific job in a pipeline.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline", "job"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"job":      map[string]any{"type": "string", "description": "Job name"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
				"limit":    map[string]any{"type": "integer", "description": "Max number of builds to return (default 10)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input listBuildsInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			limit := input.Limit
			if limit <= 0 {
				limit = 10
			}
			builds, _, _, err := defaultTeam.JobBuilds(
				atc.PipelineRef{Name: input.Pipeline},
				input.Job,
				concourse.Page{Limit: limit},
			)
			if err != nil {
				return nil, fmt.Errorf("listing builds: %w", err)
			}
			return toBuildInfoList(builds), nil
		},
	)
}

// --- get_build ---

type getBuildInput struct {
	BuildID int `json:"build_id"`
}

func registerGetBuild(s *Server, client ClientAPI) {
	s.addTool("get_build",
		"Get details for a specific build by ID. Returns status, timestamps, and metadata.",
		mustJSON(map[string]any{
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
			build, found, err := client.Build(strconv.Itoa(input.BuildID))
			if err != nil {
				return nil, fmt.Errorf("getting build: %w", err)
			}
			if !found {
				return nil, fmt.Errorf("build %d not found", input.BuildID)
			}
			return toBuildInfo(build), nil
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

func registerGetBuildLog(s *Server, client ClientAPI) {
	s.addTool("get_build_log",
		"Get the log output for a build. Returns the full build log as text.",
		mustJSON(map[string]any{
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
			events, err := client.BuildEvents(strconv.Itoa(input.BuildID))
			if err != nil {
				return nil, fmt.Errorf("connecting to build events: %w", err)
			}
			defer events.Close()

			var log strings.Builder
			for {
				ev, err := events.NextEvent()
				if err != nil {
					if err == io.EOF {
						break
					}
					return nil, fmt.Errorf("reading events: %w", err)
				}
				if logEv, ok := ev.(event.Log); ok {
					log.WriteString(logEv.Payload)
				}
			}
			return getBuildLogOutput{Log: log.String()}, nil
		},
	)
}

// --- trigger_job ---

type triggerJobInput struct {
	Pipeline string `json:"pipeline"`
	Job      string `json:"job"`
	Team     string `json:"team,omitempty"`
}

type triggerJobOutput struct {
	BuildID   int    `json:"build_id"`
	BuildName string `json:"build_name"`
	URL       string `json:"url"`
}

func registerTriggerJob(s *Server, client ClientAPI, defaultTeam TeamAPI, apiURL string) {
	s.addTool("trigger_job",
		"Trigger a new build of a job. Returns the build ID for polling status.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline", "job"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"job":      map[string]any{"type": "string", "description": "Job name"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input triggerJobInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			t, err := resolveTeam(client, defaultTeam, input.Team)
			if err != nil {
				return nil, err
			}
			build, err := t.CreateJobBuild(atc.PipelineRef{Name: input.Pipeline}, input.Job)
			if err != nil {
				return nil, fmt.Errorf("triggering job: %w", err)
			}
			url := fmt.Sprintf("%s/teams/%s/pipelines/%s/jobs/%s/builds/%s",
				apiURL, build.TeamName, input.Pipeline, input.Job, build.Name)
			return triggerJobOutput{
				BuildID:   build.ID,
				BuildName: build.Name,
				URL:       url,
			}, nil
		},
	)
}

// --- set_pipeline ---

type setPipelineInput struct {
	Pipeline string `json:"pipeline"`
	YAML     string `json:"yaml"`
	Team     string `json:"team,omitempty"`
}

type setPipelineOutput struct {
	Created  bool     `json:"created"`
	Updated  bool     `json:"updated"`
	Warnings []string `json:"warnings,omitempty"`
}

func registerSetPipeline(s *Server, defaultTeam TeamAPI) {
	s.addTool("set_pipeline",
		"Create or update a pipeline from YAML configuration. Returns whether it was created or updated.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline", "yaml"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"yaml":     map[string]any{"type": "string", "description": "Pipeline YAML configuration"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input setPipelineInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			// Get current config version for update (empty string for create).
			// Errors here are non-fatal — an empty version means "create new".
			_, version, found, err := defaultTeam.PipelineConfig(atc.PipelineRef{Name: input.Pipeline})
			if err != nil || !found {
				version = ""
			}

			created, updated, warnings, err := defaultTeam.CreateOrUpdatePipelineConfig(
				atc.PipelineRef{Name: input.Pipeline},
				version,
				[]byte(input.YAML),
				false,
			)
			if err != nil {
				return nil, fmt.Errorf("setting pipeline: %w", err)
			}
			var warningMsgs []string
			for _, w := range warnings {
				warningMsgs = append(warningMsgs, w.Message)
			}
			return setPipelineOutput{Created: created, Updated: updated, Warnings: warningMsgs}, nil
		},
	)
}

// --- abort_build ---

type abortBuildInput struct {
	BuildID int `json:"build_id"`
}

type abortBuildOutput struct {
	Success bool `json:"success"`
}

func registerAbortBuild(s *Server, client ClientAPI) {
	s.addTool("abort_build",
		"Abort a running build by its ID.",
		mustJSON(map[string]any{
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
			err := client.AbortBuild(strconv.Itoa(input.BuildID))
			if err != nil {
				return nil, fmt.Errorf("aborting build: %w", err)
			}
			return abortBuildOutput{Success: true}, nil
		},
	)
}

// --- pause_pipeline ---

type pipelineActionInput struct {
	Pipeline string `json:"pipeline"`
	Team     string `json:"team,omitempty"`
}

type pipelineActionOutput struct {
	Success bool `json:"success"`
}

func registerPausePipeline(s *Server, client ClientAPI, defaultTeam TeamAPI) {
	s.addTool("pause_pipeline",
		"Pause a pipeline, preventing new builds from being triggered.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input pipelineActionInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			t, err := resolveTeam(client, defaultTeam, input.Team)
			if err != nil {
				return nil, err
			}
			_, err = t.PausePipeline(atc.PipelineRef{Name: input.Pipeline})
			if err != nil {
				return nil, fmt.Errorf("pausing pipeline: %w", err)
			}
			return pipelineActionOutput{Success: true}, nil
		},
	)
}

// --- unpause_pipeline ---

func registerUnpausePipeline(s *Server, client ClientAPI, defaultTeam TeamAPI) {
	s.addTool("unpause_pipeline",
		"Unpause a pipeline, allowing new builds to be triggered.",
		mustJSON(map[string]any{
			"type":     "object",
			"required": []string{"pipeline"},
			"properties": map[string]any{
				"pipeline": map[string]any{"type": "string", "description": "Pipeline name"},
				"team":     map[string]any{"type": "string", "description": "Team name (defaults to target's team)"},
			},
		}),
		func(ctx context.Context, args json.RawMessage) (any, error) {
			var input pipelineActionInput
			if err := json.Unmarshal(args, &input); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			t, err := resolveTeam(client, defaultTeam, input.Team)
			if err != nil {
				return nil, err
			}
			_, err = t.UnpausePipeline(atc.PipelineRef{Name: input.Pipeline})
			if err != nil {
				return nil, fmt.Errorf("unpausing pipeline: %w", err)
			}
			return pipelineActionOutput{Success: true}, nil
		},
	)
}

// --- helpers ---

func resolveTeam(client ClientAPI, defaultTeam TeamAPI, teamName string) (TeamAPI, error) {
	if teamName == "" || teamName == defaultTeam.Name() {
		return defaultTeam, nil
	}
	t, err := client.FindTeam(teamName)
	if err != nil {
		return nil, fmt.Errorf("finding team %q: %w", teamName, err)
	}
	return t, nil
}

func toBuildInfo(b atc.Build) buildInfo {
	var dur int64
	if b.StartTime > 0 && b.EndTime > 0 {
		dur = b.EndTime - b.StartTime
	}
	return buildInfo{
		ID:              b.ID,
		Name:            b.Name,
		Status:          string(b.Status),
		PipelineName:    b.PipelineName,
		JobName:         b.JobName,
		TeamName:        b.TeamName,
		StartTime:       b.StartTime,
		EndTime:         b.EndTime,
		DurationSeconds: dur,
	}
}

func toBuildInfoList(builds []atc.Build) []buildInfo {
	result := make([]buildInfo, len(builds))
	for i, b := range builds {
		result[i] = toBuildInfo(b)
	}
	return result
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
