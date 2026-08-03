package runner

import (
	"context"
	"time"

	schema "github.com/concourse/concourse/agent/schema"
)

// SetClaudeWaitDelay overrides the claude pipe-drain bound for tests and
// returns a restore func.
func SetClaudeWaitDelay(d time.Duration) (restore func()) {
	old := claudeWaitDelay
	claudeWaitDelay = d
	return func() { claudeWaitDelay = old }
}

// SetModelEgressPreflight overrides the pre-model reachability probe and
// returns a restore func. The suite disables it by default so tests never
// depend on the host having outbound network.
func SetModelEgressPreflight(fn func(ctx context.Context, hostPort string) error) (restore func()) {
	old := modelEgressPreflight
	modelEgressPreflight = fn
	return func() { modelEgressPreflight = old }
}

// MaxTranscriptBytes exposes the transcript tail bound to tests.
const MaxTranscriptBytes = maxTranscriptBytes

// WriteTranscript exposes writeTranscript to tests.
func WriteTranscript(flightDir string, raw []byte) error {
	return writeTranscript(flightDir, raw)
}

// ParseEnvelope exposes parseEnvelope to tests.
func ParseEnvelope(out []byte) (schema.CLIEnvelope, error) {
	return parseEnvelope(out)
}
