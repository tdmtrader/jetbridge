package schema

import (
	"fmt"
	"math"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

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
	CategoryGate            Category = "gate"  // objectively-proven gate failure (§6.4.1)
	CategoryJudge           Category = "judge" // judge-cited advisory finding (§6.4.1)
)

var validCategories = map[Category]bool{
	CategorySecurity:        true,
	CategoryCorrectness:     true,
	CategoryPerformance:     true,
	CategoryMaintainability: true,
	CategoryTesting:         true,
	CategoryGate:            true,
	CategoryJudge:           true,
}

// Validate checks that the category is a known value.
func (c Category) Validate() error {
	if !validCategories[c] {
		return fmt.Errorf("invalid category %q: must be one of security, correctness, performance, maintainability, testing, gate, judge", c)
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

// ValidateSnapshotV1 applies the authoritative review/v1 snapshot contract.
// Validate intentionally remains compatibility-oriented for legacy callers.
func (r *ReviewOutput) ValidateSnapshotV1() error {
	if r == nil {
		return fmt.Errorf("review is required")
	}
	if r.SchemaVersion != "1.0.0" {
		return fmt.Errorf("schema_version must be exactly 1.0.0")
	}
	if strings.TrimSpace(r.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if err := r.Metadata.validateSnapshotV1(); err != nil {
		return err
	}
	if err := r.Score.validateSnapshotV1(); err != nil {
		return err
	}

	issueSeverities := make(map[string]Severity, len(r.ProvenIssues))
	allIDs := make(map[string]struct{}, len(r.ProvenIssues)+len(r.Observations))
	for i := range r.ProvenIssues {
		issue := &r.ProvenIssues[i]
		if err := issue.validateSnapshotV1(); err != nil {
			return fmt.Errorf("proven_issues[%d]: %w", i, err)
		}
		if _, found := allIDs[issue.ID]; found {
			return fmt.Errorf("proven_issues[%d]: duplicate finding id %q", i, issue.ID)
		}
		allIDs[issue.ID] = struct{}{}
		issueSeverities[issue.ID] = issue.Severity
	}
	for i := range r.Observations {
		observation := &r.Observations[i]
		if err := observation.validateSnapshotV1(); err != nil {
			return fmt.Errorf("observations[%d]: %w", i, err)
		}
		if _, found := allIDs[observation.ID]; found {
			return fmt.Errorf("observations[%d]: duplicate finding id %q", i, observation.ID)
		}
		allIDs[observation.ID] = struct{}{}
	}
	for i, deduction := range r.Score.Deductions {
		if strings.TrimSpace(deduction.IssueID) == "" {
			return fmt.Errorf("score.deductions[%d].issue_id is required", i)
		}
		severity, found := issueSeverities[deduction.IssueID]
		if !found {
			return fmt.Errorf("score.deductions[%d].issue_id %q does not reference a proven issue", i, deduction.IssueID)
		}
		if err := deduction.Severity.Validate(); err != nil {
			return fmt.Errorf("score.deductions[%d].severity: %w", i, err)
		}
		if deduction.Severity != severity {
			return fmt.Errorf("score.deductions[%d].severity does not match issue %q", i, deduction.IssueID)
		}
		if !finite(deduction.Points) {
			return fmt.Errorf("score.deductions[%d].points must be finite", i)
		}
	}
	if err := r.TestSummary.validateSnapshotV1(); err != nil {
		return err
	}
	if r.Metadata.TestsGenerated != r.TestSummary.TotalGenerated {
		return fmt.Errorf("metadata.tests_generated must equal test_summary.total_generated")
	}
	if r.Metadata.TestsFailing != r.TestSummary.Failing {
		return fmt.Errorf("metadata.tests_failing must equal test_summary.failing")
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

func (m Metadata) validateSnapshotV1() error {
	for name, value := range map[string]string{
		"repo": m.Repo, "commit": m.Commit, "branch": m.Branch,
		"timestamp": m.Timestamp, "agent_cli": m.AgentCLI, "agent_model": m.AgentModel,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("metadata.%s is required", name)
		}
	}
	if _, err := time.Parse(time.RFC3339, m.Timestamp); err != nil {
		return fmt.Errorf("metadata.timestamp must be RFC3339: %w", err)
	}
	for name, value := range map[string]int{
		"duration_seconds": m.DurationSec, "files_reviewed": m.FilesReviewed,
		"tests_generated": m.TestsGenerated, "tests_failing": m.TestsFailing,
	} {
		if value < 0 {
			return fmt.Errorf("metadata.%s must be nonnegative", name)
		}
	}
	if m.TestsFailing > m.TestsGenerated {
		return fmt.Errorf("metadata.tests_failing cannot exceed metadata.tests_generated")
	}
	return nil
}

// Score represents the computed review score.
type Score struct {
	Value      float64          `json:"value"`
	Max        float64          `json:"max"`
	Pass       bool             `json:"pass"`
	Threshold  float64          `json:"threshold"`
	Deductions []ScoreDeduction `json:"deductions"`
}

func (s Score) validateSnapshotV1() error {
	if !finite(s.Max) || s.Max <= 0 {
		return fmt.Errorf("score.max must be finite and positive")
	}
	if !finite(s.Value) || s.Value < 0 || s.Value > s.Max {
		return fmt.Errorf("score.value must be finite and between zero and score.max")
	}
	if !finite(s.Threshold) || s.Threshold < 0 || s.Threshold > s.Max {
		return fmt.Errorf("score.threshold must be finite and between zero and score.max")
	}
	if s.Pass && s.Value < s.Threshold {
		return fmt.Errorf("score.pass cannot be true when score.value is below score.threshold")
	}
	return nil
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

func (p *ProvenIssue) validateSnapshotV1() error {
	if p == nil {
		return fmt.Errorf("proven issue is required")
	}
	for name, value := range map[string]string{
		"id": p.ID, "title": p.Title, "file": p.File,
		"test_file": p.TestFile, "test_name": p.TestName,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if err := p.Severity.Validate(); err != nil {
		return err
	}
	if err := p.Category.Validate(); err != nil {
		return err
	}
	if err := validateSnapshotPath("file", p.File); err != nil {
		return err
	}
	if err := validateSnapshotPath("test_file", p.TestFile); err != nil {
		return err
	}
	if p.Line <= 0 {
		return fmt.Errorf("line must be positive")
	}
	if p.EndLine < 0 || (p.EndLine != 0 && p.EndLine < p.Line) {
		return fmt.Errorf("end_line must be zero or at least line")
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

func (o *Observation) validateSnapshotV1() error {
	if o == nil {
		return fmt.Errorf("observation is required")
	}
	for name, value := range map[string]string{"id": o.ID, "title": o.Title, "file": o.File} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if err := o.Category.Validate(); err != nil {
		return err
	}
	if err := validateSnapshotPath("file", o.File); err != nil {
		return err
	}
	if o.Line <= 0 {
		return fmt.Errorf("line must be positive")
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

func (ts TestSummary) validateSnapshotV1() error {
	for name, value := range map[string]int{
		"total_generated": ts.TotalGenerated,
		"passing":         ts.Passing,
		"failing":         ts.Failing,
		"error":           ts.Error,
	} {
		if value < 0 {
			return fmt.Errorf("test_summary.%s must be nonnegative", name)
		}
	}
	if ts.Passing > ts.TotalGenerated || ts.Failing > ts.TotalGenerated-ts.Passing || ts.Error != ts.TotalGenerated-ts.Passing-ts.Failing {
		return fmt.Errorf("test_summary.total_generated must equal passing + failing + error")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateSnapshotPath(label, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", label)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be valid UTF-8 without NUL", label)
	}
	if strings.Contains(value, `\`) || path.IsAbs(value) || path.Clean(value) != value || value == "." {
		return fmt.Errorf("%s must be a canonical relative POSIX path", label)
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." || (len(segment) >= 2 && ((segment[0] >= 'a' && segment[0] <= 'z') || (segment[0] >= 'A' && segment[0] <= 'Z')) && segment[1] == ':') {
			return fmt.Errorf("%s must be a canonical relative POSIX path", label)
		}
	}
	return nil
}
