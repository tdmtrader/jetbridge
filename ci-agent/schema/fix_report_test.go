package schema_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/concourse/ci-agent/schema"
)

var _ = Describe("FixReport", func() {
	Describe("JSON round-trip", func() {
		It("marshals and unmarshals a complete FixReport", func() {
			report := schema.FixReport{
				SchemaVersion: "1.0.0",
				Metadata: schema.FixMetadata{
					Repo:       "https://github.com/org/repo.git",
					BaseCommit: "abc123",
					FixBranch:  "fix/agent-review-abc123",
					HeadCommit: "def456",
					Timestamp:  "2026-02-09T17:00:00Z",
					DurationSec: 120,
					AgentCLI:   "claude-code",
					ReviewFile: "review/review.json",
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
			Expect(err).NotTo(HaveOccurred())

			var decoded schema.FixReport
			Expect(json.Unmarshal(data, &decoded)).To(Succeed())

			Expect(decoded.SchemaVersion).To(Equal("1.0.0"))
			Expect(decoded.Metadata.Repo).To(Equal("https://github.com/org/repo.git"))
			Expect(decoded.Metadata.BaseCommit).To(Equal("abc123"))
			Expect(decoded.Fixes).To(HaveLen(1))
			Expect(decoded.Fixes[0].IssueID).To(Equal("ISS-001"))
			Expect(decoded.Fixes[0].FilesChanged).To(Equal([]string{"config/loader.go"}))
			Expect(decoded.Skipped).To(HaveLen(1))
			Expect(decoded.Skipped[0].Reason).To(Equal(schema.SkipFailedVerification))
			Expect(decoded.Summary.Fixed).To(Equal(1))
		})
	})

	Describe("Validate", func() {
		It("passes for a valid report", func() {
			report := validFixReport()
			Expect(report.Validate()).To(Succeed())
		})

		It("fails when schema_version is empty", func() {
			report := validFixReport()
			report.SchemaVersion = ""
			Expect(report.Validate()).To(MatchError(ContainSubstring("schema_version")))
		})

		It("fails when metadata.repo is empty", func() {
			report := validFixReport()
			report.Metadata.Repo = ""
			Expect(report.Validate()).To(MatchError(ContainSubstring("repo")))
		})

		It("fails when metadata.base_commit is empty", func() {
			report := validFixReport()
			report.Metadata.BaseCommit = ""
			Expect(report.Validate()).To(MatchError(ContainSubstring("base_commit")))
		})

		It("fails with invalid skip reason", func() {
			report := validFixReport()
			report.Skipped = []schema.FixSkipped{
				{IssueID: "ISS-002", Status: "skipped", Reason: "invalid_reason", Attempts: 1},
			}
			Expect(report.Validate()).To(MatchError(ContainSubstring("skip reason")))
		})

		It("passes with all valid skip reasons", func() {
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
				Expect(report.Validate()).To(Succeed(), "expected reason %q to be valid", reason)
			}
		})
	})

	Describe("ExitCode", func() {
		It("returns 0 when at least one fix and regression_free", func() {
			report := validFixReport()
			report.Summary.Fixed = 1
			report.Summary.RegressionFree = true
			Expect(report.ExitCode()).To(Equal(0))
		})

		It("returns 1 when no fixes applied", func() {
			report := validFixReport()
			report.Summary.Fixed = 0
			report.Summary.RegressionFree = true
			Expect(report.ExitCode()).To(Equal(1))
		})

		It("returns 1 when regressions detected", func() {
			report := validFixReport()
			report.Summary.Fixed = 3
			report.Summary.RegressionFree = false
			Expect(report.ExitCode()).To(Equal(1))
		})
	})
})

func validFixReport() schema.FixReport {
	return schema.FixReport{
		SchemaVersion: "1.0.0",
		Metadata: schema.FixMetadata{
			Repo:       "https://github.com/org/repo.git",
			BaseCommit: "abc123",
			FixBranch:  "fix/agent-review-abc123",
			HeadCommit: "def456",
			Timestamp:  "2026-02-09T17:00:00Z",
			DurationSec: 120,
			AgentCLI:   "claude-code",
			ReviewFile: "review/review.json",
		},
		Fixes: []schema.FixApplied{
			{IssueID: "ISS-001", Status: "fixed", CommitSHA: "aaa111", FilesChanged: []string{"f.go"}, TestPassed: true, Attempts: 1},
		},
		Summary: schema.FixSummary{TotalIssues: 1, Fixed: 1, RegressionFree: true},
	}
}
