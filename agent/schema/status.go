package schema

// Three-way run/step status taxonomy used by the DB and APIs
// (shared-contracts conventions: "agent did badly" != "platform broke").
// results.json keeps its v1.0 wire values (pass/fail/error/abstain);
// this mapping is the only bridge between the two vocabularies.
const (
	RunStatusOK     = "ok"
	RunStatusFailed = "failed"
	RunStatusError  = "error"
	// RunStatusIncomplete marks an ingestion that read NO flight output (L-1,
	// #41): the step produced no results/events/review — dominant cause is a
	// runner image predating the flight recorder. A missing RECORDING, not a
	// failed step; DeriveOutcome renders it amber 'unrecorded' on a succeeded
	// build (never red).
	RunStatusIncomplete = "incomplete"
)

// ThreeWayStatus maps a results.json Status onto the three-way taxonomy.
// abstain maps to failed with abstained=true so callers can record
// `"abstained": true` metadata. Unknown values map to error.
func ThreeWayStatus(s Status) (status string, abstained bool) {
	switch s {
	case StatusPass:
		return RunStatusOK, false
	case StatusFail:
		return RunStatusFailed, false
	case StatusError:
		return RunStatusError, false
	case StatusAbstain:
		return RunStatusFailed, true
	default:
		return RunStatusError, false
	}
}
