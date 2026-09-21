package atc

import (
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// RunResultBinding names the exact generation and the claim retained by the
// durable Run header. It contains no grant or credential.
type RunResultBinding struct {
	Ref     hangar.TreeRef `json:"ref"`
	ClaimID output.ClaimID `json:"claim_id"`
}

// RunTerminalResult is published atomically and remains available after the
// disposable build payload is reclaimed. Non-success has an explicit empty
// Results map; a running Run has no terminal observation.
type RunTerminalResult struct {
	Status      RunStatus                   `json:"status"`
	CompletedAt time.Time                   `json:"completed_at"`
	Results     map[string]RunResultBinding `json:"results"`
	Version     string                      `json:"version"`
}
