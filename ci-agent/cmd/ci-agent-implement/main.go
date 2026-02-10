package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/concourse/ci-agent/implement/adapter/claude"
	"github.com/concourse/ci-agent/implement/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

func main() {
	specDir := os.Getenv("SPEC_DIR")
	repoDir := os.Getenv("REPO_DIR")
	outputDir := os.Getenv("OUTPUT_DIR")
	agentCLI := envOrDefault("AGENT_CLI", "claude")
	branchName := os.Getenv("BRANCH_NAME")
	testCmd := envOrDefault("TEST_CMD", "go test ./...")
	maxRetries := envIntOrDefault("MAX_RETRIES", 2)
	maxConsecFail := envIntOrDefault("MAX_CONSECUTIVE_FAILURES", 3)
	confThreshold := envFloatOrDefault("CONFIDENCE_THRESHOLD", 0.7)
	timeout := envDurationOrDefault("TIMEOUT", 30*time.Minute)

	if specDir == "" || repoDir == "" || outputDir == "" {
		fmt.Fprintf(os.Stderr, "SPEC_DIR, REPO_DIR, and OUTPUT_DIR are required\n")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	adapter := claude.New(agentCLI)

	opts := orchestrator.Options{
		SpecDir:                specDir,
		RepoDir:                repoDir,
		OutputDir:              outputDir,
		Adapter:                adapter,
		AgentCLI:               agentCLI,
		BranchName:             branchName,
		TestCmd:                testCmd,
		MaxRetries:             maxRetries,
		MaxConsecutiveFailures: maxConsecFail,
		ConfidenceThreshold:    confThreshold,
		Timeout:                timeout,
	}

	results, err := orchestrator.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Implementation complete: status=%s confidence=%.2f\n", results.Status, results.Confidence)

	if results.Status == schema.StatusFail || results.Status == schema.StatusError {
		os.Exit(1)
	}
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func envIntOrDefault(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func envFloatOrDefault(key string, defaultVal float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func envDurationOrDefault(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return defaultVal
}
