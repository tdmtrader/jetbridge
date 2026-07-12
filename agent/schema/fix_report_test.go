package schema_test

import (
	"encoding/json"
	"testing"

	schema "github.com/concourse/concourse/agent/schema"
)

func TestFixReportJSONRoundTrip(t *testing.T) {
	// marshals and unmarshals a complete FixReport
	report := schema.FixReport{
		SchemaVersion: "1.0.0",
		Metadata: schema.FixMetadata{
			Repo:        "https://github.com/org/repo.git",
			BaseCommit:  "abc123",
			FixBranch:   "fix/agent-review-abc123",
			HeadCommit:  "def456",
			Timestamp:   "2026-02-09T17:00:00Z",
			DurationSec: 120,
			AgentCLI:    "claude-code",
			ReviewFile:  "review/review.json",
		},
		Fixes: []schema.FixApplied{
			{
				IssueID:      "ISS-001",
				Status:       "fixed",
				CommitSHA:    "aaa111",
				FilesChanged: []string{"config/loader.go"},
				TestPassed:   true,
				Attempts:     1,
			},
		},
		Skipped: []schema.FixSkipped{
			{
				IssueID:   "ISS-002",
				Status:    "skipped",
				Reason:    schema.SkipFailedVerification,
				Attempts:  2,
				LastError: "test still fails after 2 attempts",
			},
		},
		Summary: schema.FixSummary{
			TotalIssues:    2,
			Fixed:          1,
			Skipped:        1,
			RegressionFree: true,
			ExitCode:       0,
		},
	}

	data, err := json.Marshal(report)
	requireNoErr(t, err)

	var decoded schema.FixReport
	requireNoErr(t, json.Unmarshal(data, &decoded))

	requireEqual(t, decoded.SchemaVersion, "1.0.0")
	requireEqual(t, decoded.Metadata.Repo, "https://github.com/org/repo.git")
	requireEqual(t, decoded.Metadata.BaseCommit, "abc123")
	requireLen(t, decoded.Fixes, 1)
	requireEqual(t, decoded.Fixes[0].IssueID, "ISS-001")
	requireEqual(t, decoded.Fixes[0].FilesChanged, []string{"config/loader.go"})
	requireLen(t, decoded.Skipped, 1)
	requireEqual(t, decoded.Skipped[0].Reason, schema.SkipFailedVerification)
	requireEqual(t, decoded.Summary.Fixed, 1)
}

func TestFixReportValidate(t *testing.T) {
	t.Run("passes for a valid report", func(t *testing.T) {
		report := validFixReport()
		requireNoErr(t, report.Validate())
	})

	t.Run("fails when schema_version is empty", func(t *testing.T) {
		report := validFixReport()
		report.SchemaVersion = ""
		requireErrContains(t, report.Validate(), "schema_version")
	})

	t.Run("fails when metadata.repo is empty", func(t *testing.T) {
		report := validFixReport()
		report.Metadata.Repo = ""
		requireErrContains(t, report.Validate(), "repo")
	})

	t.Run("fails when metadata.base_commit is empty", func(t *testing.T) {
		report := validFixReport()
		report.Metadata.BaseCommit = ""
		requireErrContains(t, report.Validate(), "base_commit")
	})

	t.Run("fails with invalid skip reason", func(t *testing.T) {
		report := validFixReport()
		report.Skipped = []schema.FixSkipped{
			{IssueID: "ISS-002", Status: "skipped", Reason: "invalid_reason", Attempts: 1},
		}
		requireErrContains(t, report.Validate(), "skip reason")
	})

	t.Run("passes with all valid skip reasons", func(t *testing.T) {
		for _, reason := range []schema.SkipReason{
			schema.SkipFailedVerification,
			schema.SkipTestRegression,
			schema.SkipAgentError,
			schema.SkipCompilationError,
		} {
			report := validFixReport()
			report.Skipped = []schema.FixSkipped{
				{IssueID: "ISS-002", Status: "skipped", Reason: reason, Attempts: 1},
			}
			requireNoErr(t, report.Validate(), "expected reason %q to be valid", reason)
		}
	})
}

func TestFixReportExitCode(t *testing.T) {
	t.Run("returns 0 when at least one fix and regression_free", func(t *testing.T) {
		report := validFixReport()
		report.Summary.Fixed = 1
		report.Summary.RegressionFree = true
		requireEqual(t, report.ExitCode(), 0)
	})

	t.Run("returns 1 when no fixes applied", func(t *testing.T) {
		report := validFixReport()
		report.Summary.Fixed = 0
		report.Summary.RegressionFree = true
		requireEqual(t, report.ExitCode(), 1)
	})

	t.Run("returns 1 when regressions detected", func(t *testing.T) {
		report := validFixReport()
		report.Summary.Fixed = 3
		report.Summary.RegressionFree = false
		requireEqual(t, report.ExitCode(), 1)
	})
}

func validFixReport() schema.FixReport {
	return schema.FixReport{
		SchemaVersion: "1.0.0",
		Metadata: schema.FixMetadata{
			Repo:        "https://github.com/org/repo.git",
			BaseCommit:  "abc123",
			FixBranch:   "fix/agent-review-abc123",
			HeadCommit:  "def456",
			Timestamp:   "2026-02-09T17:00:00Z",
			DurationSec: 120,
			AgentCLI:    "claude-code",
			ReviewFile:  "review/review.json",
		},
		Fixes: []schema.FixApplied{
			{IssueID: "ISS-001", Status: "fixed", CommitSHA: "aaa111", FilesChanged: []string{"f.go"}, TestPassed: true, Attempts: 1},
		},
		Summary: schema.FixSummary{TotalIssues: 1, Fixed: 1, RegressionFree: true},
	}
}
