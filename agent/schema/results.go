package schema

import (
	"encoding/json"
	"fmt"
)

// Status represents the outcome of an agent step execution.
type Status string

const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusError   Status = "error"
	StatusAbstain Status = "abstain"
)

var validStatuses = map[Status]bool{
	StatusPass:    true,
	StatusFail:    true,
	StatusError:   true,
	StatusAbstain: true,
}

// Results is the top-level schema for results.json — the structured summary
// of an agent step's outcome.
type Results struct {
	SchemaVersion string                 `json:"schema_version"`
	Status        Status                 `json:"status"`
	Confidence    float64                `json:"confidence"`
	Summary       string                 `json:"summary"`
	Artifacts     []Artifact             `json:"artifacts"`
	Metadata      map[string]interface{} `json:"metadata,omitempty"`
}

// Artifact describes a file produced by the agent step.
type Artifact struct {
	Name      string                 `json:"name"`
	Path      string                 `json:"path"`
	MediaType string                 `json:"media_type"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// Validate checks that all required fields are present and values are within
// acceptable ranges. It returns nil if the Results is valid.
func (r *Results) Validate() error {
	if r.SchemaVersion == "" {
		return fmt.Errorf("schema_version is required")
	}
	if r.Status == "" {
		return fmt.Errorf("status is required")
	}
	if !validStatuses[r.Status] {
		return fmt.Errorf("invalid status %q: must be one of pass, fail, error, abstain", r.Status)
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if r.Confidence < 0.0 || r.Confidence > 1.0 {
		return fmt.Errorf("confidence must be between 0.0 and 1.0, got %f", r.Confidence)
	}
	if r.Artifacts == nil {
		return fmt.Errorf("artifacts is required (use empty array for no artifacts)")
	}
	for i, a := range r.Artifacts {
		if err := a.Validate(); err != nil {
			return fmt.Errorf("artifact[%d]: %w", i, err)
		}
	}
	return nil
}

// Validate checks that all required Artifact fields are present.
func (a *Artifact) Validate() error {
	if a.Name == "" {
		return fmt.Errorf("artifact name is required")
	}
	if a.Path == "" {
		return fmt.Errorf("artifact path is required")
	}
	if a.MediaType == "" {
		return fmt.Errorf("artifact media_type is required")
	}
	return nil
}

// MarshalJSON implements json.Marshaler. It ensures Artifacts is serialized as
// an empty array rather than null when the slice is nil.
func (r Results) MarshalJSON() ([]byte, error) {
	type Alias Results
	a := Alias(r)
	if a.Artifacts == nil {
		a.Artifacts = []Artifact{}
	}
	return json.Marshal(a)
}
