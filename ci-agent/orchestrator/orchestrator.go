package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/ci-agent/adapter"
	"github.com/concourse/ci-agent/config"
	"github.com/concourse/ci-agent/runner"
	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/scoring"
)

// Options configures the orchestrator run.
type Options struct {
	RepoDir        string
	OutputDir      string
	ConfigPath     string
	Adapter        adapter.Adapter
	Threshold      float64
	FailOnCritical bool
	DiffOnly       bool
	BaseRef        string
}

// Run executes the full review pipeline:
// load config → dispatch adapter → write test files → run tests → classify → score → write review.json
func Run(ctx context.Context, opts Options) (*schema.ReviewOutput, error) {
	start := time.Now()

	// Load config.
	cfg := config.DefaultConfig()
	if opts.ConfigPath != "" {
		data, err := os.ReadFile(opts.ConfigPath)
		if err == nil {
			loaded, err := config.LoadConfig(data)
			if err == nil {
				cfg = loaded
			}
		}
	}

	// Set threshold default.
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = 7.0
	}

	// Dispatch adapter.
	findings, adapterErr := opts.Adapter.Review(ctx, opts.RepoDir, cfg)
	if adapterErr != nil {
		// Write partial output with error metadata.
		output := &schema.ReviewOutput{
			SchemaVersion: "1.0.0",
			Score:         schema.Score{Value: 0, Max: 10, Pass: false, Threshold: threshold},
			Summary:       fmt.Sprintf("adapter error: %v", adapterErr),
		}
		writeReviewJSON(opts.OutputDir, output)
		return output, nil
	}

	// Write test files to repo and collect paths.
	testFiles := writeTestFiles(opts.RepoDir, opts.OutputDir, findings)

	// Run tests.
	results, err := runner.RunTests(ctx, opts.RepoDir, testFiles)
	if err != nil {
		return nil, fmt.Errorf("running tests: %w", err)
	}

	// Classify results using test file basenames as keys (matching findings).
	classifyResults := remapResultsByBasename(results)
	proven, observations := runner.ClassifyResults(findings, classifyResults)

	// Score.
	score := scoring.ComputeScore(proven, cfg.SeverityWeights)
	score.Threshold = threshold
	score.Pass = scoring.EvaluatePass(score, threshold, opts.FailOnCritical)

	// Build output.
	output := &schema.ReviewOutput{
		SchemaVersion: "1.0.0",
		Metadata: schema.Metadata{
			DurationSec:    int(time.Since(start).Seconds()),
			TestsGenerated: len(testFiles),
			TestsFailing:   len(proven),
		},
		Score:        score,
		ProvenIssues: proven,
		Observations: observations,
		TestSummary:  buildTestSummary(results),
		Summary:      buildSummary(score, proven, observations),
	}

	writeReviewJSON(opts.OutputDir, output)

	return output, nil
}

// writeTestFiles writes agent-generated test files to both the repo (for execution)
// and the output directory (for archival). Returns the absolute paths of test files in the repo.
func writeTestFiles(repoDir, outputDir string, findings []runner.AgentFinding) []string {
	testsDir := filepath.Join(outputDir, "tests")
	os.MkdirAll(testsDir, 0755)

	var testFiles []string
	for _, f := range findings {
		if f.TestFile == "" || f.TestCode == "" {
			continue
		}

		// Write to repo for execution.
		repoPath := filepath.Join(repoDir, f.TestFile)
		os.WriteFile(repoPath, []byte(f.TestCode), 0644)

		// Write to output dir for archival.
		outPath := filepath.Join(testsDir, filepath.Base(f.TestFile))
		os.WriteFile(outPath, []byte(f.TestCode), 0644)

		testFiles = append(testFiles, repoPath)
	}
	return testFiles
}

// remapResultsByBasename re-keys results from absolute paths to basenames
// so they match AgentFinding.TestFile.
func remapResultsByBasename(results map[string]*runner.TestResult) map[string]*runner.TestResult {
	mapped := make(map[string]*runner.TestResult, len(results))
	for k, v := range results {
		mapped[filepath.Base(k)] = v
	}
	return mapped
}

func buildTestSummary(results map[string]*runner.TestResult) schema.TestSummary {
	var ts schema.TestSummary
	ts.TotalGenerated = len(results)
	for _, r := range results {
		switch {
		case r.Pass:
			ts.Passing++
		case r.Error:
			ts.Error++
		default:
			ts.Failing++
		}
	}
	return ts
}

func buildSummary(score schema.Score, proven []schema.ProvenIssue, observations []schema.Observation) string {
	if score.Pass {
		return fmt.Sprintf("Review passed with score %.1f/%g. %d proven issues, %d observations.",
			score.Value, score.Max, len(proven), len(observations))
	}
	return fmt.Sprintf("Review failed with score %.1f/%g (threshold: %g). %d proven issues, %d observations.",
		score.Value, score.Max, score.Threshold, len(proven), len(observations))
}

func writeReviewJSON(outputDir string, output *schema.ReviewOutput) {
	os.MkdirAll(outputDir, 0755)
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(filepath.Join(outputDir, "review.json"), data, 0644)
}
