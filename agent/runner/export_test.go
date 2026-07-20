package runner

import (
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
