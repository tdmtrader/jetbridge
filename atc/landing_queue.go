package atc

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// LandingQueueConfig is the YAML a team sets for one landing queue: the
// repository and trunk it lands on, and the templates whose Runs do the
// composing and landing. gates is accepted empty only until the gate track
// lands; a non-empty list is refused naming the key.
type LandingQueueConfig struct {
	Repository string             `json:"repository"`
	Trunk      string             `json:"trunk"`
	Compose    string             `json:"compose"`
	Land       string             `json:"land"`
	Gates      []LandingQueueGate `json:"gates,omitempty"`
}

// LandingQueueGate is one gate a queue declares: a template.
// deferred: gates and their params, track landing_queue_unit_test_gate.
type LandingQueueGate struct {
	Template string `json:"template"`
}

// ParseLandingQueueConfig decodes a queue's YAML strictly: an unknown key is
// refused naming it, and each required key is refused by name when missing.
func ParseLandingQueueConfig(body []byte) (LandingQueueConfig, error) {
	var config LandingQueueConfig
	if err := yaml.UnmarshalStrict(body, &config, yaml.DisallowUnknownFields); err != nil {
		return LandingQueueConfig{}, fmt.Errorf("landing queue config: %w", err)
	}
	return config, config.Validate()
}

// Validate names the first missing required key, or the gates key while gates
// are not yet supported.
func (config LandingQueueConfig) Validate() error {
	for _, required := range []struct{ key, value string }{
		{"repository", config.Repository},
		{"trunk", config.Trunk},
		{"compose", config.Compose},
		{"land", config.Land},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("landing queue config: %s is required", required.key)
		}
	}
	if !validTrunk(config.Trunk) {
		return errors.New("landing queue config: trunk is a branch name: letters, digits, '.', '_', '-' and '/', not starting with '-' or 'refs/'")
	}
	if !validRepository(config.Repository) {
		return errors.New("landing queue config: repository is a URL starting with https://, ssh://, git@, file:// or /")
	}
	if len(config.Gates) != 0 {
		return errors.New("landing queue config: gates are not supported yet; set an empty list")
	}
	return nil
}

// LandingEntryState is where an entry is: queued, in flight, or settled as
// landed or ejected.
type LandingEntryState string

const (
	LandingEntryQueued   LandingEntryState = "queued"
	LandingEntryInFlight LandingEntryState = "in_flight"
	LandingEntryLanded   LandingEntryState = "landed"
	LandingEntryEjected  LandingEntryState = "ejected"
)

// LandingSubmission is what fly submits: the entry's id and the commit the
// submit ref points at.
type LandingSubmission struct {
	ID     string `json:"id"`
	Commit string `json:"commit"`
}

// LandingEntry is one entry as the API presents it, with its settle record.
type LandingEntry struct {
	ID           string            `json:"id"`
	Commit       string            `json:"commit"`
	State        LandingEntryState `json:"state"`
	SubmittedBy  string            `json:"submitted_by,omitempty"`
	SubmittedAt  time.Time         `json:"submitted_at"`
	SettledAt    *time.Time        `json:"settled_at,omitempty"`
	SettleReason string            `json:"settle_reason,omitempty"`
	ComposeRun   int               `json:"compose_run,omitempty"`
	LandRun      int               `json:"land_run,omitempty"`
}

// LandingQueueStatus is a queue with its entries and the state of its
// landings: how many land Runs in a row did not land, and the last error.
type LandingQueueStatus struct {
	Name        string             `json:"name"`
	Config      LandingQueueConfig `json:"config"`
	Entries     []LandingEntry     `json:"entries"`
	FailedLands int                `json:"failed_lands"`
	LastError   string             `json:"last_error,omitempty"`
}

// ValidLandingEntryID is the submit ref's one component: letters, digits,
// dot, underscore and dash, starting with a letter or digit, at most 100 runes.
func ValidLandingEntryID(id string) bool {
	if id == "" || len(id) > 100 || strings.HasSuffix(id, ".lock") || strings.Contains(id, "..") {
		return false
	}
	for i, r := range id {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if i == 0 && !alnum {
			return false
		}
		if !alnum && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

// ValidCommitSHA is a full 40-hex-digit sha.
func ValidCommitSHA(sha string) bool {
	if len(sha) != 40 {
		return false
	}
	for _, r := range sha {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// validTrunk is a branch name the templates can pass to git as a ref
// component without it reading as an option or escaping the refs/heads
// namespace.
func validTrunk(trunk string) bool {
	if trunk == "" || strings.HasPrefix(trunk, "-") || strings.HasPrefix(trunk, "refs/") || strings.Contains(trunk, "..") || strings.HasSuffix(trunk, "/") || strings.HasSuffix(trunk, ".lock") {
		return false
	}
	for _, r := range trunk {
		alnum := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !alnum && r != '.' && r != '_' && r != '-' && r != '/' {
			return false
		}
	}
	return true
}

// validRepository is a remote the templates can pass to git without it
// reading as an option.
func validRepository(repository string) bool {
	for _, prefix := range []string{"https://", "ssh://", "git@", "file:///", "/"} {
		if strings.HasPrefix(repository, prefix) {
			return !strings.ContainsAny(repository, " \t\n\"'")
		}
	}
	return false
}
