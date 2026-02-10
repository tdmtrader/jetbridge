package implement

import (
	"fmt"

	"github.com/concourse/ci-agent/schema"
)

// ResultsOpts provides metadata for results building.
type ResultsOpts struct {
	RepoDir  string
	AgentCLI string
	Branch   string
}

// BuildResults creates a schema.Results from tracker state and confidence.
func BuildResults(tracker *TaskTracker, conf *ConfidenceResult, opts ResultsOpts) *schema.Results {
	summary := tracker.Summary()

	status := schema.StatusPass
	switch conf.Status {
	case "fail":
		status = schema.StatusFail
	case "abstain":
		status = schema.StatusAbstain
	case "error":
		status = schema.StatusError
	}

	summaryText := fmt.Sprintf(
		"Implementation complete: %d/%d tasks committed, %d skipped, %d failed.",
		summary.Committed, summary.Total, summary.Skipped, summary.Failed,
	)

	artifacts := []schema.Artifact{
		{Name: "summary", Path: "summary.md", MediaType: "text/markdown"},
		{Name: "progress", Path: "progress.json", MediaType: "application/json"},
	}

	metadata := map[string]string{}
	if opts.RepoDir != "" {
		metadata["repo_dir"] = opts.RepoDir
	}
	if opts.AgentCLI != "" {
		metadata["agent_cli"] = opts.AgentCLI
	}
	if opts.Branch != "" {
		metadata["branch"] = opts.Branch
	}

	return &schema.Results{
		SchemaVersion: "1.0",
		Status:        status,
		Confidence:    conf.Score,
		Summary:       summaryText,
		Artifacts:     artifacts,
		Metadata:      metadata,
	}
}
