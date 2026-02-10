package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/concourse/ci-agent/fix"
	fixclaude "github.com/concourse/ci-agent/fix/adapter/claude"
)

func main() {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	report, err := fix.RunFixPipeline(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "fix pipeline error: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("Fix complete: %d fixed, %d skipped out of %d issues\n",
		report.Summary.Fixed, report.Summary.Skipped, report.Summary.TotalIssues)

	if report.Summary.RegressionFree {
		fmt.Println("Regression guard: PASS")
	} else {
		fmt.Println("Regression guard: FAIL — fixes reverted")
	}

	os.Exit(report.ExitCode())
}

func parseOptions() (fix.FixOptions, error) {
	repoDir := envOrDefault("REPO_DIR", "repo")
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return fix.FixOptions{}, fmt.Errorf("repo dir %q does not exist", repoDir)
	}

	reviewDir := envOrDefault("REVIEW_DIR", "review")
	if _, err := os.Stat(reviewDir); os.IsNotExist(err) {
		return fix.FixOptions{}, fmt.Errorf("review dir %q does not exist", reviewDir)
	}

	outputDir := envOrDefault("OUTPUT_DIR", "fix-report")
	os.MkdirAll(outputDir, 0755)

	fixBranch := envOrDefault("FIX_BRANCH", "")
	testCommand := envOrDefault("TEST_COMMAND", "go test ./...")

	maxRetries := 2
	if s := os.Getenv("MAX_RETRIES"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v > 0 {
			maxRetries = v
		}
	}

	agentCLI := envOrDefault("AGENT_CLI", "claude")
	agentModel := os.Getenv("AGENT_MODEL")

	return fix.FixOptions{
		RepoDir:     repoDir,
		ReviewDir:   reviewDir,
		OutputDir:   outputDir,
		Adapter:     fixclaude.New(agentCLI, agentModel),
		FixBranch:   fixBranch,
		MaxRetries:  maxRetries,
		TestCommand: testCommand,
	}, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
