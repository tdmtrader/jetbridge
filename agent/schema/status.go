package schema

// Three-way run/step status taxonomy used by the DB and APIs
// (shared-contracts conventions: "agent did badly" != "platform broke").
// results.json keeps its v1.0 wire values (pass/fail/error/abstain);
// this mapping is the only bridge between the two vocabularies.
const (
	RunStatusOK     = "ok"
	RunStatusFailed = "failed"
	RunStatusError  = "error"
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
