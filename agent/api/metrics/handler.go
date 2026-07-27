package metrics

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"

	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/tedsuo/rata"
)

// workflowNamePattern mirrors agent/api/workflowoutcomes: the workflow-name
// path segment must be a valid workflow identifier before it reaches the store.
var workflowNamePattern = regexp.MustCompile(`^[\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d][\p{Ll}\p{Lt}\p{Lm}\p{Lo}\d\-_.]{0,127}$`)

// Handler serves the agent run-metrics read routes. Auth is enforced by the
// wrappa layer (authorized viewer) — the handler trusts the request. Metrics
// are written in-process by the agent step, never over HTTP.
type Handler struct {
	store Store
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// ListRecent handles GET /api/v1/agent/metrics?limit=N — the most-recent agent
// run metrics across all workflow runs/builds, newest-first (operator dashboard).
func (h *Handler) ListRecent(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, _ = strconv.Atoi(v) // invalid/absent → 0 → store applies its default
	}
	rows, err := h.store.ListRecent(limit)
	if err != nil {
		http.Error(w, "failed to list metrics", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []schema.RunMetrics{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

// ListByBuild handles GET /api/v1/builds/:build_id/agent-metrics.
func (h *Handler) ListByBuild(w http.ResponseWriter, r *http.Request) {
	buildID, err := strconv.Atoi(r.FormValue(":build_id"))
	if err != nil || buildID <= 0 {
		http.Error(w, "invalid build_id", http.StatusBadRequest)
		return
	}
	rows, err := h.store.GetByBuild(buildID)
	if err != nil {
		http.Error(w, "failed to list metrics", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []schema.RunMetrics{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}

// ListByWorkflowRun handles
// GET /api/v1/agent/workflows/:workflow_name/runs/:workflow_run_id/metrics —
// the metrics of one durable workflow run. Both the workflow name and the
// run id are validated and both scope the store query (identity + authz).
func (h *Handler) ListByWorkflowRun(w http.ResponseWriter, r *http.Request) {
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
	rows, err := h.store.ListByWorkflowRun(workflowName, runID)
	if err != nil {
		http.Error(w, "failed to list metrics", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []schema.RunMetrics{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)
}
