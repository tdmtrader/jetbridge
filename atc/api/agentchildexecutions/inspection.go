package agentchildexecutions

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

// NewInspectionHandler exposes a deliberately read-only, team-bound execution
// view. The team ID is supplied by ATC's normal team-scoped authorization
// wrapper, never by a client header or query parameter.
func NewInspectionHandler(store ExecutionStore, teamID int) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || store == nil || teamID <= 0 {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		execution, found, err := store.Find(request.Context(), teamID, request.URL.Query().Get(":execution_id"))
		if err != nil || !found || execution.TeamID != teamID {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(inspectionFor(execution))
	})
}

func NewTeamInspectionHandlerFactory(store ExecutionStore) func(db.Team) http.Handler {
	return func(team db.Team) http.Handler { return NewInspectionHandler(store, team.ID()) }
}

// Inspection intentionally omits prompt text, native transcript data,
// credentials, capabilities, and provider error payloads. The persisted
// identity, lifecycle, observed accounting and sealed snapshot are enough to
// diagnose a disconnected synchronous caller.
type Inspection struct {
	ID                string                `json:"id"`
	Tool              string                `json:"tool"`
	Tier              string                `json:"tier"`
	Effort            string                `json:"effort"`
	ProfileID         string                `json:"profile_id"`
	ProfileDigest     string                `json:"profile_digest"`
	State             string                `json:"state"`
	Sequence          int64                 `json:"sequence"`
	ResultSnapshotID  *int64                `json:"result_snapshot_id,omitempty"`
	WorkspaceSnapshot *snapshot.SnapshotRef `json:"workspace_snapshot,omitempty"`
	DurationMS        *int64                `json:"duration_ms,omitempty"`
	ErrorCode         string                `json:"error_code,omitempty"`
	ErrorRetryable    *bool                 `json:"error_retryable,omitempty"`
	ErrorSummary      string                `json:"error_summary,omitempty"`
	StaticReview      bool                  `json:"static_review"`
	TestsRun          bool                  `json:"tests_run"`
}

func inspectionFor(execution db.AgentChildExecution) Inspection {
	inspection := Inspection{}
	inspection.ID = execution.ID
	inspection.Tool = string(execution.Tool)
	inspection.Tier = string(execution.Selector.Tier)
	inspection.Effort = string(execution.Selector.Effort)
	inspection.ProfileID = execution.ProfileID
	inspection.ProfileDigest = execution.ProfileDigest
	inspection.State = string(execution.State)
	inspection.Sequence = execution.Sequence
	inspection.ResultSnapshotID = execution.ResultSnapshotID
	inspection.WorkspaceSnapshot = execution.WorkspaceSnapshot
	inspection.DurationMS = execution.DurationMS
	inspection.ErrorCode = execution.ErrorCode
	inspection.ErrorRetryable = execution.ErrorRetryable
	inspection.ErrorSummary = execution.ErrorSummary
	inspection.StaticReview = execution.Tool == "request_review"
	inspection.TestsRun = false
	return inspection
}
