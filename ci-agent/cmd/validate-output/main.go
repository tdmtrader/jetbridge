package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/concourse/ci-agent/schema"
)

func main() {
	outputDir := flag.String("output-dir", "", "Directory containing agent output files")
	outputType := flag.String("type", "", "Output type to validate: review, fix, plan, qa, implement")
	flag.Parse()

	if *outputDir == "" || *outputType == "" {
		fmt.Fprintf(os.Stderr, "FAIL: --output-dir and --type are required\n")
		os.Exit(1)
	}

	if err := validate(*outputDir, *outputType); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %s\n", err)
		os.Exit(1)
	}

	fmt.Printf("PASS: %s output is valid\n", *outputType)
}

func validate(dir, outputType string) error {
	switch outputType {
	case "review":
		return validateFile(dir, "review.json", func(data []byte) error {
			var v schema.ReviewOutput
			if err := json.Unmarshal(data, &v); err != nil {
				return fmt.Errorf("failed to parse review.json: %w", err)
			}
			return v.Validate()
		})
	case "fix":
		return validateFile(dir, "fix-report.json", func(data []byte) error {
			var v schema.FixReport
			if err := json.Unmarshal(data, &v); err != nil {
				return fmt.Errorf("failed to parse fix-report.json: %w", err)
			}
			return v.Validate()
		})
	case "plan", "implement":
		return validateFile(dir, "results.json", func(data []byte) error {
			var v schema.Results
			if err := json.Unmarshal(data, &v); err != nil {
				return fmt.Errorf("failed to parse results.json: %w", err)
			}
			return v.Validate()
		})
	case "qa":
		return validateFile(dir, "qa.json", func(data []byte) error {
			var v schema.QAOutput
			if err := json.Unmarshal(data, &v); err != nil {
				return fmt.Errorf("failed to parse qa.json: %w", err)
			}
			return v.Validate()
		})
	default:
		return fmt.Errorf("unknown output type %q: must be one of review, fix, plan, qa, implement", outputType)
	}
}

func validateFile(dir, filename string, fn func([]byte) error) error {
	path := filepath.Join(dir, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", filename, err)
	}
	return fn(data)
}
