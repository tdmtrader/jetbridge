package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/concourse/ci-agent/adapter/claude"
	"github.com/concourse/ci-agent/orchestrator"
)

func main() {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := orchestrator.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "orchestrator error: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("Review complete: score=%.1f/%g pass=%v\n", result.Score.Value, result.Score.Max, result.Score.Pass)
	fmt.Printf("Proven issues: %d, Observations: %d\n", len(result.ProvenIssues), len(result.Observations))
	fmt.Println(result.Summary)

	if !result.Score.Pass {
		os.Exit(1)
	}
}

func parseOptions() (orchestrator.Options, error) {
	repoDir := envOrDefault("REPO_DIR", "repo")
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return orchestrator.Options{}, fmt.Errorf("repo dir %q does not exist", repoDir)
	}

	outputDir := envOrDefault("OUTPUT_DIR", "review")
	os.MkdirAll(outputDir, 0755)

	agentCLI := envOrDefault("AGENT_CLI", "claude")
	agentModel := envOrDefault("AGENT_MODEL", "")
	configPath := envOrDefault("REVIEW_CONFIG", "")

	threshold := 7.0
	if s := os.Getenv("SCORE_THRESHOLD"); s != "" {
		if v, err := strconv.ParseFloat(s, 64); err == nil {
			threshold = v
		}
	}

	failOnCritical := envOrDefault("FAIL_ON_CRITICAL", "false") == "true"
	diffOnly := envOrDefault("REVIEW_DIFF_ONLY", "false") == "true"
	baseRef := envOrDefault("BASE_REF", "main")

	adapter := claude.New(agentCLI, agentModel)

	return orchestrator.Options{
		RepoDir:        repoDir,
		OutputDir:      outputDir,
		ConfigPath:     configPath,
		Adapter:        adapter,
		Threshold:      threshold,
		FailOnCritical: failOnCritical,
		DiffOnly:       diffOnly,
		BaseRef:        baseRef,
	}, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
