package transcriptserver

import (
	"io"
	"net/http"
	"regexp"

	"code.cloudfoundry.org/lager/v3"
	"github.com/tedsuo/rata"

	"github.com/concourse/concourse/agent/snapshot"
)

// workflowNamePattern mirrors agent/api/metrics: the workflow-name shape the
// rest of the v3 API validates before it reaches a store query.
var workflowNamePattern = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]{0,127}$`)

// GetTranscript serves GET
// /api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/transcripts/:plan_id
// — the raw tool-call transcript (ndjson) the runner captured for one agent
// step of a durable workflow run. The workflow name and the run id are both
// validated and both scope the store query (identity + authz); the plan id
// selects the step WITHIN that run, so a plan id belonging to another run is
// never readable through this route. 404 when the run has no transcript for
// that plan.
func (s *Server) GetTranscript(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.Session("get-agent-workflow-run-transcript")

	workflowName := rata.Param(r, "workflow_name")
	if !workflowNamePattern.MatchString(workflowName) {
		http.Error(w, "invalid workflow name", http.StatusBadRequest)
		return
	}
	runID, err := snapshot.ParseWorkflowRunID(rata.Param(r, "workflow_run_id"))
	if err != nil {
		http.Error(w, "invalid workflow run ID", http.StatusBadRequest)
		return
	}
	planID := rata.Param(r, "plan_id")
	if planID == "" {
		http.Error(w, "invalid plan ID", http.StatusBadRequest)
		return
	}

	if s.store == nil {
		http.Error(w, "transcript API is not enabled", http.StatusNotFound)
		return
	}

	rows, err := s.store.ListByWorkflowRun(workflowName, runID)
	if err != nil {
		logger.Error("failed-to-get-transcript", err, lager.Data{
			"workflow_name":   workflowName,
			"workflow_run_id": int64(runID),
			"plan_id":         planID,
		})
		http.Error(w, "failed to get transcript", http.StatusInternalServerError)
		return
	}

	for _, row := range rows {
		if row.PlanID != planID {
			continue
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, row.NDJSON)
		return
	}

	http.Error(w, "no transcript available for plan", http.StatusNotFound)
}
