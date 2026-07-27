package transcriptserver

import (
	"encoding/json"
	"net/http"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
)

// Ref is the index entry for one persisted agent transcript: enough for the
// run page to know WHICH steps of a run are inspectable (and how big their
// transcript is) without shipping every ndjson body up front. The body is
// fetched per plan through GetAgentWorkflowRunTranscript.
type Ref struct {
	PlanID     string `json:"plan_id"`
	FunctionID string `json:"function_id"`
	StepName   string `json:"step_name"`
	BuildID    int    `json:"build_id"`
	// WorkflowRunID is marshaled as a quoted string: it is a 64-bit id and
	// the Elm/JS client would lose precision on a JSON number.
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	ByteLen       int    `json:"byte_len"`
	Truncated     bool   `json:"truncated"`
}

// ListTranscripts serves GET
// /api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/transcripts —
// the index of transcripts captured for one durable workflow run, oldest-first.
// Same scope rule as the body route: the workflow name and the run id together
// are the identity + authz check, so a run id addressed under the wrong
// workflow name lists nothing.
func (s *Server) ListTranscripts(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.Session("list-agent-workflow-run-transcripts")

	workflowName, runID, ok := runScope(w, r)
	if !ok {
		return
	}

	refs := []Ref{}
	if s.store != nil {
		rows, err := s.store.ListByWorkflowRun(workflowName, runID)
		if err != nil {
			logger.Error("failed-to-list-transcripts", err, lager.Data{
				"workflow_name":   workflowName,
				"workflow_run_id": int64(runID),
			})
			http.Error(w, "failed to list transcripts", http.StatusInternalServerError)
			return
		}
		for _, row := range rows {
			ref := Ref{
				PlanID:     row.PlanID,
				FunctionID: row.FunctionID,
				StepName:   row.StepName,
				BuildID:    row.BuildID,
				ByteLen:    row.ByteLen,
				Truncated:  row.Truncated,
			}
			if row.WorkflowRunID != nil {
				ref.WorkflowRunID = strconv.FormatInt(int64(*row.WorkflowRunID), 10)
			}
			refs = append(refs, ref)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(refs)
}
