package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/concourse/ci-agent/schema"
)

// WriteSpec writes spec markdown to the output directory and returns an artifact.
func WriteSpec(outputDir, content string) (*schema.Artifact, error) {
	path := filepath.Join(outputDir, "spec.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write spec.md: %w", err)
	}
	return &schema.Artifact{
		Name:      "spec",
		Path:      "spec.md",
		MediaType: "text/markdown",
	}, nil
}

// WritePlan writes plan markdown to the output directory and returns an artifact.
func WritePlan(outputDir, content string) (*schema.Artifact, error) {
	path := filepath.Join(outputDir, "plan.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return nil, fmt.Errorf("write plan.md: %w", err)
	}
	return &schema.Artifact{
		Name:      "plan",
		Path:      "plan.md",
		MediaType: "text/markdown",
	}, nil
}

// WriteResults writes results.json to the output directory.
func WriteResults(outputDir string, results *schema.Results) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	path := filepath.Join(outputDir, "results.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write results.json: %w", err)
	}
	return nil
}
