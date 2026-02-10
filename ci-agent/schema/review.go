package schema

import "fmt"

// Severity represents the impact level of a proven issue.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
)

var validSeverities = map[Severity]bool{
	SeverityCritical: true,
	SeverityHigh:     true,
	SeverityMedium:   true,
	SeverityLow:      true,
}

// Validate checks that the severity is a known value.
func (s Severity) Validate() error {
	if !validSeverities[s] {
		return fmt.Errorf("invalid severity %q: must be one of critical, high, medium, low", s)
	}
	return nil
}

// Category classifies what kind of concern a finding addresses.
type Category string

const (
	CategorySecurity        Category = "security"
	CategoryCorrectness     Category = "correctness"
	CategoryPerformance     Category = "performance"
	CategoryMaintainability Category = "maintainability"
	CategoryTesting         Category = "testing"
)

var validCategories = map[Category]bool{
	CategorySecurity:        true,
	CategoryCorrectness:     true,
	CategoryPerformance:     true,
	CategoryMaintainability: true,
	CategoryTesting:         true,
}

// Validate checks that the category is a known value.
func (c Category) Validate() error {
	if !validCategories[c] {
		return fmt.Errorf("invalid category %q: must be one of security, correctness, performance, maintainability, testing", c)
	}
	return nil
}

// ReviewOutput is the top-level schema for review.json (v1.0.0).
type ReviewOutput struct {
	SchemaVersion string        `json:"schema_version"`
	Metadata      Metadata      `json:"metadata"`
	Score         Score         `json:"score"`
	ProvenIssues  []ProvenIssue `json:"proven_issues"`
	Observations  []Observation `json:"observations"`
	TestSummary   TestSummary   `json:"test_summary"`
	Summary       string        `json:"summary"`
}

// Validate checks that all required fields are present.
func (r *ReviewOutput) Validate() error {
	if r.SchemaVersion == "" {
		return fmt.Errorf("schema_version is required")
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

// Metadata captures context about the review execution.
type Metadata struct {
	Repo           string `json:"repo"`
	Commit         string `json:"commit"`
	Branch         string `json:"branch"`
	Timestamp      string `json:"timestamp"`
	DurationSec    int    `json:"duration_seconds"`
	AgentCLI       string `json:"agent_cli"`
	AgentModel     string `json:"agent_model"`
	FilesReviewed  int    `json:"files_reviewed"`
	TestsGenerated int    `json:"tests_generated"`
	TestsFailing   int    `json:"tests_failing"`
}

// Score represents the computed review score.
type Score struct {
	Value      float64          `json:"value"`
	Max        float64          `json:"max"`
	Pass       bool             `json:"pass"`
	Threshold  float64          `json:"threshold"`
	Deductions []ScoreDeduction `json:"deductions"`
}

// PassesThreshold returns true if the score value meets or exceeds the threshold.
func (s *Score) PassesThreshold() bool {
	return s.Value >= s.Threshold
}

// ScoreDeduction records a single deduction from the base score.
type ScoreDeduction struct {
	IssueID  string   `json:"issue_id"`
	Severity Severity `json:"severity"`
	Points   float64  `json:"points"`
}

// ProvenIssue is a defect demonstrated by a failing test.
type ProvenIssue struct {
	ID          string   `json:"id"`
	Severity    Severity `json:"severity"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	EndLine     int      `json:"end_line,omitempty"`
	TestFile    string   `json:"test_file"`
	TestName    string   `json:"test_name"`
	TestOutput  string   `json:"test_output,omitempty"`
	Category    Category `json:"category"`
}

// Validate checks that all required ProvenIssue fields are present.
func (p *ProvenIssue) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("proven issue id is required")
	}
	if p.Severity == "" {
		return fmt.Errorf("proven issue severity is required")
	}
	if p.Title == "" {
		return fmt.Errorf("proven issue title is required")
	}
	if p.File == "" {
		return fmt.Errorf("proven issue file is required")
	}
	if p.Line == 0 {
		return fmt.Errorf("proven issue line is required")
	}
	if p.TestFile == "" {
		return fmt.Errorf("proven issue test_file is required")
	}
	if p.TestName == "" {
		return fmt.Errorf("proven issue test_name is required")
	}
	return nil
}

// Observation is an advisory finding without a failing test.
type Observation struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	File        string   `json:"file"`
	Line        int      `json:"line"`
	Category    Category `json:"category"`
}

// Validate checks that all required Observation fields are present.
func (o *Observation) Validate() error {
	if o.ID == "" {
		return fmt.Errorf("observation id is required")
	}
	if o.Title == "" {
		return fmt.Errorf("observation title is required")
	}
	if o.File == "" {
		return fmt.Errorf("observation file is required")
	}
	if o.Line == 0 {
		return fmt.Errorf("observation line is required")
	}
	if o.Category == "" {
		return fmt.Errorf("observation category is required")
	}
	return nil
}

// TestSummary counts of generated tests by outcome.
type TestSummary struct {
	TotalGenerated int `json:"total_generated"`
	Passing        int `json:"passing"`
	Failing        int `json:"failing"`
	Error          int `json:"error"`
}

// IsConsistent returns true if total = passing + failing + error.
func (ts *TestSummary) IsConsistent() bool {
	return ts.TotalGenerated == ts.Passing+ts.Failing+ts.Error
}
