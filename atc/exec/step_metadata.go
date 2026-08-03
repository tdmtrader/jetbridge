package exec

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/concourse/concourse/agent/snapshot"
)

type StepMetadata struct {
	BuildID              int
	BuildName            string
	TeamID               int
	TeamName             string
	JobID                int
	JobName              string
	PipelineID           int
	PipelineName         string
	PipelineInstanceVars map[string]any
	InstanceVarsQuery    url.Values
	ExternalURL          string
	CreatedBy            string
	// SnapshotCreatedBy is the authenticated producer principal used only by
	// server-side snapshot provenance. It is never exported to step env.
	SnapshotCreatedBy string
	// WorkflowDefinitionID and WorkflowRunID are the server-authenticated
	// selected-build association. They are never exported to step env.
	WorkflowDefinitionID *int
	WorkflowVersion      *int
	WorkflowRunID        *snapshot.WorkflowRunID
	// ResourceCaptureTemplate is the authenticated name of the server-owned
	// resource-capture template that owns this build's pipeline run. It is the
	// trust anchor for atc.ResourceCaptureAuthority and is set only by the
	// engine, from db.Build.ResourceCaptureTemplateAssociation. It is never
	// exported to step env.
	//
	// The template name, not the run or pipeline id, is the narrowest correct
	// signal: it is the same value the DB's capture-output authorization binds
	// the operation key to, so exec can prove the authority in the plan belongs
	// to *this* capture operation rather than merely to some server template.
	ResourceCaptureTemplate string
}

func (metadata StepMetadata) Env() []string {
	env := []string{}

	if metadata.BuildID != 0 {
		env = append(env, fmt.Sprintf("BUILD_ID=%d", metadata.BuildID))
	}

	if metadata.BuildName != "" {
		env = append(env, "BUILD_NAME="+metadata.BuildName)
	}

	if metadata.TeamID != 0 {
		env = append(env, fmt.Sprintf("BUILD_TEAM_ID=%d", metadata.TeamID))
	}

	if metadata.TeamName != "" {
		env = append(env, "BUILD_TEAM_NAME="+metadata.TeamName)
	}

	if metadata.JobID != 0 {
		env = append(env, fmt.Sprintf("BUILD_JOB_ID=%d", metadata.JobID))
	}

	if metadata.JobName != "" {
		env = append(env, "BUILD_JOB_NAME="+metadata.JobName)
	}

	if metadata.PipelineID != 0 {
		env = append(env, fmt.Sprintf("BUILD_PIPELINE_ID=%d", metadata.PipelineID))
	}

	if metadata.PipelineName != "" {
		env = append(env, "BUILD_PIPELINE_NAME="+metadata.PipelineName)
	}

	if metadata.PipelineInstanceVars != nil {
		bytes, _ := json.Marshal(metadata.PipelineInstanceVars)
		env = append(env, "BUILD_PIPELINE_INSTANCE_VARS="+string(bytes))
	}

	if metadata.ExternalURL != "" {
		env = append(env, "ATC_EXTERNAL_URL="+metadata.ExternalURL)

		if metadata.BuildID != 0 {
			buildURLShort := fmt.Sprintf("%s/builds/%d", metadata.ExternalURL, metadata.BuildID)
			buildURL := buildURLShort

			if metadata.TeamName != "" && metadata.PipelineName != "" && metadata.JobName != "" && metadata.BuildName != "" {
				buildURL = fmt.Sprintf("%s/teams/%s/pipelines/%s/jobs/%s/builds/%s",
					metadata.ExternalURL,
					metadata.TeamName,
					metadata.PipelineName,
					metadata.JobName,
					metadata.BuildName)

				if len(metadata.InstanceVarsQuery) > 0 {
					buildURL += "?" + metadata.InstanceVarsQuery.Encode()
				}
			}

			env = append(env,
				"BUILD_URL="+buildURL,
				"BUILD_URL_SHORT="+buildURLShort)
		}
	}

	if metadata.CreatedBy != "" {
		env = append(env, "BUILD_CREATED_BY="+metadata.CreatedBy)
	}

	return env
}

// TaskEnv returns the env exposed to task containers. Unlike upstream
// Concourse, this fork exposes build identity so tasks can correlate their
// results with the ATC build they ran in.
func (metadata StepMetadata) TaskEnv() []string {
	env := []string{}
	if metadata.BuildID != 0 {
		env = append(env, fmt.Sprintf("BUILD_ID=%d", metadata.BuildID))
	}
	if metadata.BuildName != "" {
		env = append(env, "BUILD_NAME="+metadata.BuildName)
	}
	if metadata.TeamName != "" {
		env = append(env, "BUILD_TEAM_NAME="+metadata.TeamName)
	}
	if metadata.JobName != "" {
		env = append(env, "BUILD_JOB_NAME="+metadata.JobName)
	}
	if metadata.PipelineName != "" {
		env = append(env, "BUILD_PIPELINE_NAME="+metadata.PipelineName)
	}
	if metadata.ExternalURL != "" {
		env = append(env, "ATC_EXTERNAL_URL="+metadata.ExternalURL)
	}
	return env
}
