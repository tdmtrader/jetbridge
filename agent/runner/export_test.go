package runner

import (
	"bytes"
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

// MaxStreamLineBytes exposes the watchdog's line bound to tests.
const MaxStreamLineBytes = maxStreamLineBytes

// WriteTranscript exposes writeTranscript to tests.
func WriteTranscript(flightDir string, raw []byte) error {
	return writeTranscript(flightDir, raw)
}

// ParseEnvelope exposes parseEnvelope to tests.
func ParseEnvelope(out []byte) (schema.CLIEnvelope, error) {
	return parseEnvelope(out)
}

// WatchdogRun is everything a test-driven watchdog observed.
type WatchdogRun struct {
	Reason              string
	Detail              string
	CostUSD             float64
	Turns               int
	Kills               int
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	UsageMessages       int
	Warnings            string
}

// ObserveStreamForTest writes chunks VERBATIM (no newline is appended, so a
// test can drive partial lines and the oversized-line drop) through a watchdog
// armed by cfg. kill, when non-nil, runs on every trip in addition to the
// bookkeeping — pass a panicking one to exercise the fail-safe.
func ObserveStreamForTest(cfg Config, kill func(), chunks ...string) WatchdogRun {
	var run WatchdogRun
	var warnings bytes.Buffer

	dog := newWatchdog(cfg, &warnings, func() {
		run.Kills++
		if kill != nil {
			kill()
		}
	})
	writer := &streamLineWriter{observe: dog.observe}
	for _, chunk := range chunks {
		_, _ = writer.Write([]byte(chunk))
	}

	run.Reason, run.Detail = dog.terminated()
	cost, turns, usage := dog.observed()
	run.CostUSD, run.Turns = cost, turns
	run.InputTokens = usage.InputTokens
	run.OutputTokens = usage.OutputTokens
	run.CacheReadTokens = usage.CacheReadInputTokens
	run.CacheCreationTokens = usage.CacheCreationInputTokens
	run.UsageMessages = usage.Messages
	run.Warnings = warnings.String()
	return run
}

// ObserveStreamLinesForTest folds the given stream-json lines (one newline
// appended to each) through a watchdog armed by cfg.
func ObserveStreamLinesForTest(cfg Config, lines ...string) WatchdogRun {
	chunks := make([]string, 0, len(lines))
	for _, line := range lines {
		chunks = append(chunks, line+"\n")
	}
	return ObserveStreamForTest(cfg, nil, chunks...)
}
