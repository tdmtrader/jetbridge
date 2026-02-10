package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/ci-agent/plan/adapter"
	"github.com/concourse/ci-agent/plan/adapter/claude"
	"github.com/concourse/ci-agent/plan/confidence"
	"github.com/concourse/ci-agent/plan/orchestrator"
	"github.com/concourse/ci-agent/schema"
)

func main() {
	opts, err := parseOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	timeout := parseDuration(os.Getenv("TIMEOUT"), 5*time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	results, err := orchestrator.Run(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "planning error: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("Planning %s (confidence: %.2f)\n", results.Status, results.Confidence)

	switch results.Status {
	case schema.StatusPass, schema.StatusAbstain:
		os.Exit(0)
	default:
		os.Exit(1)
	}
}

func parseOptions() (orchestrator.Options, error) {
	inputDir := envOrDefault("INPUT_DIR", "story")
	inputPath := filepath.Join(inputDir, "input.json")
	if _, err := os.Stat(inputPath); os.IsNotExist(err) {
		return orchestrator.Options{}, fmt.Errorf("input file %q does not exist", inputPath)
	}

	outputDir := envOrDefault("OUTPUT_DIR", "plan-output")
	os.MkdirAll(outputDir, 0755)

	agentCLI := envOrDefault("AGENT_CLI", "claude")
	agentModel := os.Getenv("AGENT_MODEL")

	threshold := parseFloat(os.Getenv("CONFIDENCE_THRESHOLD"), 0.6)
	weights := parseWeights(os.Getenv("CONFIDENCE_WEIGHTS"))

	return orchestrator.Options{
		InputPath:           inputPath,
		OutputDir:           outputDir,
		Adapter:             claude.New(agentCLI),
		ConfidenceThreshold: threshold,
		ConfidenceWeights:   weights,
		SpecOpts:            adapter.SpecOpts{Model: agentModel},
		PlanOpts:            adapter.PlanOpts{Model: agentModel},
	}, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseFloat(s string, def float64) float64 {
	if s == "" {
		return def
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return def
	}
	return f
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func parseWeights(s string) confidence.ConfidenceWeights {
	if s == "" {
		return confidence.DefaultWeights()
	}
	var w confidence.ConfidenceWeights
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return confidence.DefaultWeights()
	}
	return w
}
