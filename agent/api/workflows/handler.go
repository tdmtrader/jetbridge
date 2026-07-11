package workflows

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/api/accessor"
)

const maxDefinitionBytes = 1 << 20 // 1 MiB

// Handler serves the agent workflow-definition API (contracts §4.2:
// ListAgentWorkflows, ListAgentWorkflowVersions, GetAgentWorkflowVersion,
// CreateAgentWorkflowVersion, PromoteAgentWorkflowVersion).
type Handler struct {
	store workflow.Store
}

func NewHandler(store workflow.Store) *Handler {
	return &Handler{store: store}
}

// WorkflowSummary is the GET /api/v1/agent/workflows element.
type WorkflowSummary struct {
	Name          string `json:"name"`
	Description   string `json:"description"`
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

// List handles GET /api/v1/agent/workflows.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	defs, err := h.store.List()
	if err != nil {
		http.Error(w, "failed to list workflows", http.StatusInternalServerError)
		return
	}
	summaries := []WorkflowSummary{}
	for _, d := range defs {
		s := WorkflowSummary{
			Name:          d.Name,
			Description:   d.Description,
			LatestVersion: d.Version,
			ContentHash:   d.ContentHash,
			CreatedAt:     d.CreatedAt,
		}
		if d.Live {
			s.LiveVersion = d.Version
		} else {
			live, found, err := h.store.Live(d.Name)
			if err != nil {
				http.Error(w, "failed to resolve live version", http.StatusInternalServerError)
				return
			}
			if found {
				s.LiveVersion = live.Version
			}
		}
		summaries = append(summaries, s)
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
// The request body is the raw definition YAML; the response is the
// stored Definition (200 both for a new version and an idempotent
// content-hash hit).
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue(":workflow_name")
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
