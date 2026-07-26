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

// ObserveStreamLinesForTest folds the given stream-json lines through a
// watchdog armed by cfg and reports the winning breach reason (empty when
// none), the cumulative totals it saw, and whether it fired the kill.
func ObserveStreamLinesForTest(cfg Config, lines ...string) (reason string, cost float64, turns int, killed bool) {
	dog := newWatchdog(cfg, func() { killed = true })
	writer := &streamLineWriter{observe: dog.observe}
	for _, line := range lines {
		_, _ = writer.Write([]byte(line + "\n"))
	}
	reason, _ = dog.terminated()
	cost, turns = dog.observed()
	return reason, cost, turns, killed
}
