package workflows

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/api/accessor"
)

const maxDefinitionBytes = 1 << 20 // 1 MiB

// maxManifestRequestBytes bounds the JSON manifest envelope: 10 MiB of
// content (workflow.MaxManifestBytes) plus JSON-encoding overhead.
const maxManifestRequestBytes = 12 << 20

// Handler serves the agent workflow-definition API (contracts §4.2:
// ListAgentWorkflows, ListAgentWorkflowVersions, GetAgentWorkflowVersion,
// CreateAgentWorkflowVersion, PromoteAgentWorkflowVersion).
type Handler struct {
	store workflow.Store
	stats StatsProvider
}

// StatsProvider is the read the Stats handler needs — a subset of
// agent/api/metrics.Store, satisfied in production by
// atc/db.AgentRunMetricsFactory.
type StatsProvider interface {
	WorkflowStats(workflowName string) ([]schema.WorkflowVersionStats, error)
}

func NewHandler(store workflow.Store, stats StatsProvider) *Handler {
	return &Handler{store: store, stats: stats}
}

// WorkflowSummary is the GET /api/v1/agent/workflows element.
type WorkflowSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
	Annotation    string `json:"annotation,omitempty"`
	Hidden        bool   `json:"hidden"`
	LatestVersion int    `json:"latest_version"`
	ContentHash   string `json:"content_hash"`
	LiveVersion   int    `json:"live_version"` // 0 = no live version
	CreatedAt     int64  `json:"created_at"`
}

// requestUser mirrors accessor's userName(): preferred_username, falling
// back to name. Safe on requests without an accessor in context —
// accessor.GetAccessor returns an empty access whose Claims() are zero.
func requestUser(r *http.Request) string {
	claims := accessor.GetAccessor(r).Claims()
	if claims.PreferredUsername != "" {
		return claims.PreferredUsername
	}
	return claims.UserName
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// Summarize builds the workflow-list view: the latest version per name with
// its resolved live version. It is the single implementation behind both the
// HTTP List handler and the list_agent_workflows MCP tool, and costs exactly
// two metadata queries regardless of workflow count (previously each
// non-live-latest name triggered a Live() lookup that fetched and parsed the
// full definition YAML just to read a version number).
func Summarize(store workflow.Store) ([]WorkflowSummary, error) {
	defs, err := store.List()
	if err != nil {
		return nil, err
	}
	liveVersions, err := store.LiveVersions()
	if err != nil {
		return nil, err
	}
	summaries := []WorkflowSummary{}
	for _, d := range defs {
		summaries = append(summaries, WorkflowSummary{
			Name:          d.Name,
			Description:   d.Description,
			LatestVersion: d.Version,
			ContentHash:   d.ContentHash,
			LiveVersion:   liveVersions[d.Name], // 0 = no live version
			CreatedAt:     d.CreatedAt,
		})
	}
	return summaries, nil
}

// List handles GET /api/v1/agent/workflows.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	summaries, err := Summarize(h.store)
	if err != nil {
		http.Error(w, "failed to list workflows", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, summaries)
}

// Versions handles GET /api/v1/agent/workflows/:workflow_name/versions.
func (h *Handler) Versions(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	defs, err := h.store.Versions(name)
	if err != nil {
		http.Error(w, "failed to list versions", http.StatusInternalServerError)
		return
	}
	if len(defs) == 0 {
		http.Error(w, "unknown workflow", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, defs)
}

// Get handles GET /api/v1/agent/workflows/:workflow_name/versions/:version.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	version, err := strconv.Atoi(r.FormValue(":version"))
	if err != nil {
		http.Error(w, "version must be an integer", http.StatusBadRequest)
		return
	}
	def, found, err := h.store.Get(name, version)
	if err != nil {
		http.Error(w, "failed to get workflow version", http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "unknown workflow version", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// Import handles POST /api/v1/agent/workflows/:workflow_name/versions.
// An application/json body is a source manifest ({"files": {path:
// content}}, design 2026-07-17 §2); any other Content-Type is the raw
// definition YAML — the single-file degenerate case. The response is
// the stored Definition (200 both for a new version and an idempotent
// content-hash hit).
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")

	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxManifestRequestBytes+1))
		if err != nil {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
			return
		}
		if len(raw) > maxManifestRequestBytes {
			http.Error(w, "manifest exceeds 12 MiB", http.StatusRequestEntityTooLarge)
			return
		}
		var body struct {
			Files workflow.Manifest `json:"files"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			http.Error(w, "malformed manifest body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body.Files) == 0 {
			http.Error(w, `manifest body must carry a non-empty "files" map`, http.StatusBadRequest)
			return
		}
		def, err := h.store.ImportManifest(name, body.Files, requestUser(r))
		if err != nil {
			var inv workflow.InvalidDefinitionError
			if errors.As(err, &inv) {
				http.Error(w, inv.Error(), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to import workflow", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, def)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDefinitionBytes+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	if len(raw) == 0 {
		http.Error(w, "request body must be the definition YAML", http.StatusBadRequest)
		return
	}
	if len(raw) > maxDefinitionBytes {
		http.Error(w, "definition exceeds 1 MiB", http.StatusRequestEntityTooLarge)
		return
	}

	def, err := h.store.Import(name, raw, requestUser(r))
	if err != nil {
		var inv workflow.InvalidDefinitionError
		if errors.As(err, &inv) {
			http.Error(w, inv.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "failed to import workflow", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, def)
}

// Promote handles PUT /api/v1/agent/workflows/:workflow_name/versions/:version/live.
func (h *Handler) Promote(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	version, err := strconv.Atoi(r.FormValue(":version"))
	if err != nil {
		http.Error(w, "version must be an integer", http.StatusBadRequest)
		return
	}
	err = h.store.Promote(name, version, requestUser(r))
	if errors.Is(err, workflow.ErrVersionNotFound) {
		http.Error(w, "unknown workflow version", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "failed to promote workflow version", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Stats handles GET /api/v1/agent/workflows/:workflow_name/stats. Returns the
// per-version aggregation with the derived ratios filled in. A workflow with
// no runs returns [] (200), not 404 — the workflow may exist with zero runs.
func (h *Handler) Stats(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
	rows, err := h.stats.WorkflowStats(name)
	if err != nil {
		http.Error(w, "failed to load workflow stats", http.StatusInternalServerError)
		return
	}
	out := make([]schema.WorkflowVersionStats, len(rows))
	for i, row := range rows {
		out[i] = row.WithDerived()
	}
	writeJSON(w, http.StatusOK, out)
}
