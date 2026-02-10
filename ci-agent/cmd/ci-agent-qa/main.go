package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/concourse/ci-agent/config"
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

	output, err := orchestrator.RunQA(ctx, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "qa error: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("QA score: %.1f/%.1f (threshold: %.1f)\n",
		output.Score.Value, output.Score.Max, output.Score.Threshold)
	fmt.Printf("Requirements: %d total, %d covered\n",
		output.Metadata.RequirementsTotal, output.Metadata.RequirementsCovered)

	if output.Score.Pass {
		fmt.Println("Result: PASS")
		os.Exit(0)
	} else {
		fmt.Println("Result: FAIL")
		os.Exit(1)
	}
}

func parseOptions() (orchestrator.QAOptions, error) {
	repoDir := envOrDefault("REPO_DIR", "repo")
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return orchestrator.QAOptions{}, fmt.Errorf("repo dir %q does not exist", repoDir)
	}

	specFile := envOrDefault("SPEC_FILE", "spec/spec.md")
	if _, err := os.Stat(specFile); os.IsNotExist(err) {
		return orchestrator.QAOptions{}, fmt.Errorf("spec file %q does not exist", specFile)
	}

	outputDir := envOrDefault("OUTPUT_DIR", "qa")
	os.MkdirAll(outputDir, 0755)

	cfg := config.DefaultQAConfig()

	if threshold := os.Getenv("SCORE_THRESHOLD"); threshold != "" {
		var t float64
		if _, err := fmt.Sscanf(threshold, "%f", &t); err == nil {
			cfg.Threshold = t
		}
	}

	if genTests := os.Getenv("GENERATE_TESTS"); genTests == "false" {
		cfg.GenerateTests = false
	}

	if bp := os.Getenv("BROWSER_PLAN"); bp == "false" {
		cfg.BrowserPlan = false
	}

	targetURL := envOrDefault("TARGET_URL", "http://localhost")

	return orchestrator.QAOptions{
		RepoDir:   repoDir,
		SpecFile:  specFile,
		OutputDir: outputDir,
		Config:    cfg,
		TargetURL: targetURL,
	}, nil
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
