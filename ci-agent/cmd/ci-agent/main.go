package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/concourse/ci-agent/envconfig"
	"github.com/concourse/ci-agent/llm"
	"github.com/concourse/ci-agent/phaseconfig"
	"github.com/concourse/ci-agent/phaserunner"
	"github.com/concourse/ci-agent/schema"
	"github.com/concourse/ci-agent/tracing"
)

func main() {
	if err := tracing.Init(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: tracing init: %v\n", err)
	}
	defer tracing.Shutdown(context.Background())

	phasePath := ""
	for i, arg := range os.Args[1:] {
		if arg == "--phase" && i+1 < len(os.Args[1:])-1+1 {
			if i+2 < len(os.Args) {
				phasePath = os.Args[i+2]
			}
		}
	}

	if phasePath == "" {
		fmt.Fprintf(os.Stderr, "Usage: ci-agent --phase <path-to-phase.yaml>\n")
		os.Exit(2)
	}

	configData, err := os.ReadFile(phasePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading phase config: %v\n", err)
		os.Exit(2)
	}

	cfg, err := phaseconfig.Parse(configData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing phase config: %v\n", err)
		os.Exit(2)
	}

	// Resolve output dir from config or env
	outputDir := "output"
	if ev, ok := cfg.Env["output_dir"]; ok {
		outputDir = envconfig.StringOrDefault(ev.Var, ev.Default)
	}
	os.MkdirAll(outputDir, 0755)

	agentCLI := envconfig.StringOrDefault("AGENT_CLI", "claude")
	agentModel := os.Getenv("AGENT_MODEL")
	timeout := envconfig.DurationOrDefault("TIMEOUT", 10*time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Resolve base dir for templates relative to the phase config file
	baseDir := filepath.Dir(phasePath)
	if !filepath.IsAbs(baseDir) {
		if abs, err := filepath.Abs(baseDir); err == nil {
			baseDir = abs
		}
	}

	results, err := phaserunner.Run(ctx, phaserunner.Options{
		ConfigPath: phasePath,
		Config:     cfg,
		ConfigData: configData,
		OutputDir:  outputDir,
		Model:      agentModel,
		Client:     llm.NewClaudeClient(agentCLI),
		BaseDir:    baseDir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "phase error: %v\n", err)
		os.Exit(2)
	}

	fmt.Printf("%s: %s (confidence: %.2f)\n", cfg.Name, results.Status, results.Confidence)

	switch results.Status {
	case schema.StatusPass, schema.StatusAbstain:
		os.Exit(0)
	default:
		os.Exit(1)
	}
}
