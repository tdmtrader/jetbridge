package schema

import "fmt"

// SkipReason describes why a proven issue was not fixed.
type SkipReason string

const (
	SkipFailedVerification SkipReason = "failed_verification"
	SkipTestRegression     SkipReason = "test_regression"
	SkipAgentError         SkipReason = "agent_error"
	SkipCompilationError   SkipReason = "compilation_error"
)

var validSkipReasons = map[SkipReason]bool{
	SkipFailedVerification: true,
	SkipTestRegression:     true,
	SkipAgentError:         true,
	SkipCompilationError:   true,
}

// FixReport is the top-level schema for fix-report.json (v1.0.0).
type FixReport struct {
	SchemaVersion string       `json:"schema_version"`
	Metadata      FixMetadata  `json:"metadata"`
	Fixes         []FixApplied `json:"fixes"`
	Skipped       []FixSkipped `json:"skipped"`
	Summary       FixSummary   `json:"summary"`
}

// Validate checks that all required FixReport fields are present.
func (r *FixReport) Validate() error {
	if r.SchemaVersion == "" {
		return fmt.Errorf("schema_version is required")
	}
	if r.Metadata.Repo == "" {
		return fmt.Errorf("metadata.repo is required")
	}
	if r.Metadata.BaseCommit == "" {
		return fmt.Errorf("metadata.base_commit is required")
	}
	for _, s := range r.Skipped {
		if !validSkipReasons[s.Reason] {
			return fmt.Errorf("invalid skip reason %q", s.Reason)
		}
	}
	return nil
}

// ExitCode returns the process exit code for this report.
// Returns 0 when at least one fix was applied and no regressions; 1 otherwise.
func (r *FixReport) ExitCode() int {
	if r.Summary.Fixed > 0 && r.Summary.RegressionFree {
		return 0
	}
	return 1
}

// FixMetadata captures context about the fix execution.
type FixMetadata struct {
	Repo        string `json:"repo"`
	BaseCommit  string `json:"base_commit"`
	FixBranch   string `json:"fix_branch"`
	HeadCommit  string `json:"head_commit"`
	Timestamp   string `json:"timestamp"`
	DurationSec int    `json:"duration_seconds"`
	AgentCLI    string `json:"agent_cli"`
	ReviewFile  string `json:"review_file"`
}

// FixApplied records a successfully applied fix.
type FixApplied struct {
	IssueID      string   `json:"issue_id"`
	Status       string   `json:"status"`
	CommitSHA    string   `json:"commit_sha"`
	FilesChanged []string `json:"files_changed"`
	TestPassed   bool     `json:"test_passed"`
	Attempts     int      `json:"attempts"`
}

// FixSkipped records an issue that could not be fixed.
type FixSkipped struct {
	IssueID   string     `json:"issue_id"`
	Status    string     `json:"status"`
	Reason    SkipReason `json:"reason"`
	Attempts  int        `json:"attempts"`
	LastError string     `json:"last_error,omitempty"`
}

// FixSummary provides aggregate counts for the fix report.
type FixSummary struct {
	TotalIssues    int  `json:"total_issues"`
	Fixed          int  `json:"fixed"`
	Skipped        int  `json:"skipped"`
	RegressionFree bool `json:"regression_free"`
	ExitCode       int  `json:"exit_code"`
}
