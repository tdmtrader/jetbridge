package schema

import "fmt"

// Status represents the outcome of an agent step.
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

// Artifact describes a file produced by an agent step.
type Artifact struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	MediaType string `json:"media_type"`
}

// Results is the top-level schema for results.json (v1.0).
type Results struct {
	SchemaVersion string            `json:"schema_version"`
	Status        Status            `json:"status"`
	Confidence    float64           `json:"confidence"`
	Summary       string            `json:"summary"`
	Artifacts     []Artifact        `json:"artifacts"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// Validate checks that all required Results fields are present and valid.
func (r *Results) Validate() error {
	if r.SchemaVersion == "" {
		r.SchemaVersion = "1.0"
	}
	if r.Status == "" {
		return fmt.Errorf("status is required")
	}
	if !validStatuses[r.Status] {
		return fmt.Errorf("invalid status %q", r.Status)
	}
	if r.Summary == "" {
		return fmt.Errorf("summary is required")
	}
	if r.Confidence < 0.0 || r.Confidence > 1.0 {
		return fmt.Errorf("confidence must be between 0.0 and 1.0, got %f", r.Confidence)
	}
	if len(r.Artifacts) == 0 {
		return fmt.Errorf("at least one artifact is required")
	}
	for _, a := range r.Artifacts {
		if a.Name == "" {
			return fmt.Errorf("artifact name is required")
		}
		if a.Path == "" {
			return fmt.Errorf("artifact path is required")
		}
		if a.MediaType == "" {
			return fmt.Errorf("artifact media_type is required")
		}
	}
	return nil
}
