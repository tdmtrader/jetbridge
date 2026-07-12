package schema

// Three-way run/step status taxonomy used by the DB and APIs
// (shared-contracts conventions: "agent did badly" != "platform broke").
// results.json keeps its v1.0 wire values (pass/fail/error/abstain);
// this mapping is the only bridge between the two vocabularies.
const (
	RunStatusOK     = "ok"
	RunStatusFailed = "failed"
	RunStatusError  = "error"
	// RunStatusParked marks a park-exit partial ingestion (§1.8, 2026-07-10
	// PARK-V2 amendment): the step exited awaiting a human, with best-effort
	// usage/cost — a defined end, not an error.
	RunStatusParked = "parked"
)

// ThreeWayStatus maps a results.json Status onto the three-way taxonomy.
// abstain maps to failed with abstained=true so callers can record
// `"abstained": true` metadata. parked maps to parked (PARK-V2, §1.8) so a
// park-exit ingestion is not silently rewritten to error. Unknown values map
// to error.
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
	case StatusParked:
		return RunStatusParked, false
	default:
		return RunStatusError, false
	}
}
