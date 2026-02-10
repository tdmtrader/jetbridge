package fix

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/ci-agent/schema"
)

// FixOptions configures the fix pipeline.
type FixOptions struct {
	RepoDir     string
	ReviewDir   string
	OutputDir   string
	Adapter     FixAdapter
	FixBranch   string
	MaxRetries  int
	TestCommand string
}

// RunFixPipeline executes the full fix pipeline:
// parse review → sort issues → create branch → fix loop → regression guard → write report
func RunFixPipeline(ctx context.Context, opts FixOptions) (*schema.FixReport, error) {
	start := time.Now()

	if opts.MaxRetries == 0 {
		opts.MaxRetries = 2
	}

	// Parse review.json.
	reviewPath := filepath.Join(opts.ReviewDir, "review.json")
	review, err := ParseReviewOutput(reviewPath)
	if err != nil {
		return nil, fmt.Errorf("parsing review: %w", err)
	}

	// Get base commit.
	baseSHA, _ := GetHeadSHA(opts.RepoDir)

	// Sort issues by severity.
	issues := SortIssuesBySeverity(review.ProvenIssues)

	// Create fix branch.
	if opts.FixBranch != "" {
		if err := CreateBranch(opts.RepoDir, opts.FixBranch); err != nil {
			return nil, fmt.Errorf("creating branch: %w", err)
		}
	}

	// Copy proving tests from review into repo.
	copyProvingTests(opts.ReviewDir, opts.RepoDir, issues)

	// Fix loop.
	engine := NewEngine(opts.Adapter, opts.MaxRetries)
	var fixes []schema.FixApplied
	var skipped []schema.FixSkipped

	for _, issue := range issues {
		// Read the proving test code.
		testCode := readFileContent(filepath.Join(opts.RepoDir, issue.TestFile))

		result := engine.FixSingleIssue(ctx, opts.RepoDir, issue, testCode)

		if result.Status == "fixed" {
			fixes = append(fixes, schema.FixApplied{
				IssueID:      result.IssueID,
				Status:       "fixed",
				CommitSHA:    result.CommitSHA,
				FilesChanged: result.FilesChanged,
				TestPassed:   true,
				Attempts:     result.Attempts,
			})
		} else {
			skipped = append(skipped, schema.FixSkipped{
				IssueID:   result.IssueID,
				Status:    "skipped",
				Reason:    result.Reason,
				Attempts:  result.Attempts,
				LastError: result.LastError,
			})
		}
	}

	// Regression guard.
	regressionFree := true
	if len(fixes) > 0 && opts.TestCommand != "" {
		suiteResult, err := RunFullTestSuite(ctx, opts.RepoDir, opts.TestCommand)
		if err == nil && !suiteResult.Pass {
			regressionFree = false
			// Rollback: revert each fix commit and mark as skipped.
			for i := len(fixes) - 1; i >= 0; i-- {
				if revertErr := RevertLastCommit(opts.RepoDir); revertErr == nil {
					skipped = append(skipped, schema.FixSkipped{
						IssueID:   fixes[i].IssueID,
						Status:    "skipped",
						Reason:    schema.SkipTestRegression,
						Attempts:  fixes[i].Attempts,
						LastError: "caused test regression",
					})
				}
			}
			fixes = nil
		}
	}

	headSHA, _ := GetHeadSHA(opts.RepoDir)

	report := &schema.FixReport{
		SchemaVersion: "1.0.0",
		Metadata: schema.FixMetadata{
			Repo:        opts.RepoDir,
			BaseCommit:  baseSHA,
			FixBranch:   opts.FixBranch,
			HeadCommit:  headSHA,
			Timestamp:   time.Now().UTC().Format(time.RFC3339),
			DurationSec: int(time.Since(start).Seconds()),
			AgentCLI:    "claude-code",
			ReviewFile:  reviewPath,
		},
		Fixes:   fixes,
		Skipped: skipped,
		Summary: schema.FixSummary{
			TotalIssues:    len(issues),
			Fixed:          len(fixes),
			Skipped:        len(skipped),
			RegressionFree: regressionFree,
		},
	}

	// Write fix-report.json.
	os.MkdirAll(opts.OutputDir, 0755)
	data, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(filepath.Join(opts.OutputDir, "fix-report.json"), data, 0644)

	return report, nil
}

// copyProvingTests copies test files from review/tests/ into the repo.
func copyProvingTests(reviewDir, repoDir string, issues []schema.ProvenIssue) {
	testsDir := filepath.Join(reviewDir, "tests")
	for _, issue := range issues {
		src := filepath.Join(testsDir, filepath.Base(issue.TestFile))
		dst := filepath.Join(repoDir, issue.TestFile)
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		os.WriteFile(dst, data, 0644)
	}
}
