package atc

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// LandingQueueConfig is the YAML a team sets for one landing queue: the
// repository and trunk it lands on, the templates whose Runs do the composing
// and landing, and the gates a candidate must pass in between. A queue with no
// gate is a complete queue: compose, then land.
type LandingQueueConfig struct {
	Repository string             `json:"repository"`
	Trunk      string             `json:"trunk"`
	Compose    string             `json:"compose"`
	Land       string             `json:"land"`
	Gates      []LandingQueueGate `json:"gates,omitempty"`
}

// LandingQueueGate is one gate a queue declares: a template the queue admits
// a Run of for every candidate, with the compose Run's composition bound as
// its `composition` run input and the queue's repository, trunk and entries as
// its params, and how long that Run may take before it counts as no verdict.
// Wait is a Go duration ("45m"); empty means DefaultLandingGateWait.
type LandingQueueGate struct {
	Template string `json:"template"`
	Wait     string `json:"wait,omitempty"`
}

// DefaultLandingGateWait is how long a gate Run may take when its gate names
// no wait: the pipeline's own unit-tests task budget.
const DefaultLandingGateWait = 45 * time.Minute

// MaxLandingGateWait bounds a gate's wait, so a typo cannot park a queue for
// a week.
const MaxLandingGateWait = 24 * time.Hour

// WaitDuration is the gate's wait cap. Validate refuses anything it cannot
// parse, so an unparsable value here falls back to the default.
func (gate LandingQueueGate) WaitDuration() time.Duration {
	wait, err := time.ParseDuration(gate.Wait)
	if gate.Wait == "" || err != nil || wait < time.Second {
		return DefaultLandingGateWait
	}
	return wait
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

// Validate names the first missing required key, or the first gate that is
// not usable. That each gate's template exists on the team and is a template
// is the API's check, which can see the team.
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
		return errors.New("landing queue config: repository is a URL starting with https://, ssh://, git@, file:/// or /")
	}
	seen := map[string]bool{}
	for i, gate := range config.Gates {
		switch {
		case strings.TrimSpace(gate.Template) == "":
			return fmt.Errorf("landing queue config: gates[%d].template is required", i)
		case strings.ContainsAny(gate.Template, " \t\n/"):
			return fmt.Errorf("landing queue config: gates[%d].template %q is not a pipeline name", i, gate.Template)
		case seen[gate.Template]:
			return fmt.Errorf("landing queue config: gates[%d].template %q is declared twice", i, gate.Template)
		case gate.Template == config.Compose || gate.Template == config.Land:
			return fmt.Errorf("landing queue config: gates[%d].template %q is the queue's compose or land template", i, gate.Template)
		}
		seen[gate.Template] = true
		if gate.Wait == "" {
			continue
		}
		if wait, err := time.ParseDuration(gate.Wait); err != nil || wait < time.Second || wait > MaxLandingGateWait {
			return fmt.Errorf("landing queue config: gates[%d].wait %q is not a duration from 1s to %s, like 45m", i, gate.Wait, MaxLandingGateWait)
		}
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

// LandingGateVerdict is where one gate of a candidate is: pending (its Run is
// admitted and running, or is about to be admitted again), passed, failed (a
// red verdict, which ejected the entry), or no_verdict (its Run errored,
// aborted or overran its wait twice in a row, and the queue is paused on it).
type LandingGateVerdict string

const (
	LandingGatePending   LandingGateVerdict = "pending"
	LandingGatePassed    LandingGateVerdict = "passed"
	LandingGateFailed    LandingGateVerdict = "failed"
	LandingGateNoVerdict LandingGateVerdict = "no_verdict"
)

// LandingGate is one gate of an entry's candidate as the API presents it:
// the template, its latest Run's number, its verdict, how many Runs it has
// had, and why the last one did not count.
type LandingGate struct {
	Template  string             `json:"template"`
	Run       int                `json:"run,omitempty"`
	Verdict   LandingGateVerdict `json:"verdict"`
	Attempts  int                `json:"attempts"`
	LastError string             `json:"last_error,omitempty"`
}

// LandingEntry is one entry as the API presents it, with its settle record
// and the gates of its latest candidate.
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
	Gates        []LandingGate     `json:"gates,omitempty"`
}

// LandingQueueStatus is a queue with its entries and the state of its
// landings: whether it is paused and why, how many land Runs in a row did not
// land, and the last error.
type LandingQueueStatus struct {
	Name        string             `json:"name"`
	Config      LandingQueueConfig `json:"config"`
	Paused      bool               `json:"paused"`
	PauseReason string             `json:"pause_reason,omitempty"`
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
