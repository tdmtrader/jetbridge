package plan

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/concourse/ci-agent/schema"
)

// ParseInput reads a PlanningInput from an io.Reader.
func ParseInput(r io.Reader) (*schema.PlanningInput, error) {
	var input schema.PlanningInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return nil, fmt.Errorf("parse input: %w", err)
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	return &input, nil
}

// ParseInputFile reads a PlanningInput from a JSON file.
func ParseInputFile(path string) (*schema.PlanningInput, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input file: %w", err)
	}
	defer f.Close()
	return ParseInput(f)
}
