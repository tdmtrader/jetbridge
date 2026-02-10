package fix

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/concourse/ci-agent/schema"
)

const supportedSchemaVersion = "1.0.0"

// ParseReviewOutput reads and parses a review.json file from disk.
func ParseReviewOutput(path string) (*schema.ReviewOutput, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading review file: %w", err)
	}

	var output schema.ReviewOutput
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, fmt.Errorf("parsing review JSON: %w", err)
	}

	if output.SchemaVersion != supportedSchemaVersion {
		return nil, fmt.Errorf("unsupported schema version %q (expected %q)", output.SchemaVersion, supportedSchemaVersion)
	}

	return &output, nil
}

// severityOrder maps severity to a numeric rank for sorting (lower = higher priority).
var severityOrder = map[schema.Severity]int{
	schema.SeverityCritical: 0,
	schema.SeverityHigh:     1,
	schema.SeverityMedium:   2,
	schema.SeverityLow:      3,
}

// SortIssuesBySeverity sorts ProvenIssues by severity (critical first),
// then by file path for determinism within the same severity.
func SortIssuesBySeverity(issues []schema.ProvenIssue) []schema.ProvenIssue {
	if len(issues) == 0 {
		return []schema.ProvenIssue{}
	}

	sorted := make([]schema.ProvenIssue, len(issues))
	copy(sorted, issues)

	sort.Slice(sorted, func(i, j int) bool {
		oi := severityOrder[sorted[i].Severity]
		oj := severityOrder[sorted[j].Severity]
		if oi != oj {
			return oi < oj
		}
		return sorted[i].File < sorted[j].File
	})

	return sorted
}
