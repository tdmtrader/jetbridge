package outcomes

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/gitcheck"
	"github.com/concourse/concourse/agent/projection"
)

// MirrorProvider opens the repo mirror and returns a windowed diff.
// Implemented by the outcome watcher's MirrorCache — one cache shared by
// the watcher component and this handler. A nil provider means the diff
// API is disabled (no --agent-outcome-git-dir, the master switch).
type MirrorProvider interface {
	Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error)
}

// TicketRepositoryChangeResolution distinguishes a projection-native attempt
// from a legacy v1/v2 attempt. A missing native projection must not silently
// fall back to mutable live Git state.
type TicketRepositoryChangeResolution struct {
	Change projection.RepositoryChange
	Found  bool
	Legacy bool
}

type TicketRepositoryChangeResolver interface {
	ResolveTicketRepositoryChange(context.Context, int) (TicketRepositoryChangeResolution, error)
}

// DiffHandler serves GetAgentTicketDiff. Window numbers are frozen in
// §1.11.1: 50-file default window, 64 KiB per-file cap (enforced by
// gitcheck), has_more paging. It pins base_sha..pushed_sha permanently —
// unlike the external compare link, it survives the merge and works for
// private/non-GitHub remotes (both stay).
type DiffHandler struct {
	store    Store
	provider MirrorProvider
	resolver TicketRepositoryChangeResolver
}

func NewDiffHandler(store Store, provider MirrorProvider) *DiffHandler {
	return &DiffHandler{store: store, provider: provider}
}

func NewDiffHandlerWithProjection(store Store, provider MirrorProvider, resolver TicketRepositoryChangeResolver) *DiffHandler {
	return &DiffHandler{store: store, provider: provider, resolver: resolver}
}

// GetDiff handles GET /api/v1/agent/tickets/:ticket_id/diff. 404s when the
// API is disabled, no outcome row exists, or base_sha is unknown (a
// fallback-seeded row — the exception now that harvest seeds base_sha at
// push time): the ATC never renders unbounded diffs.
func (h *DiffHandler) GetDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	legacy := h.resolver == nil
	if h.resolver != nil {
		resolution, err := h.resolver.ResolveTicketRepositoryChange(r.Context(), id)
		if err != nil {
			http.Error(w, "diff projection lookup failed", http.StatusInternalServerError)
			return
		}
		legacy = resolution.Legacy
		if resolution.Found {
			if resolution.Change.Status != projection.RepositoryChangeProjectionReady {
				http.Error(w, "repository-change diff is not ready", http.StatusNotFound)
				return
			}
			page, err := projectedDiffPage(resolution.Change, offset, limit)
			if err != nil {
				http.Error(w, "repository-change projection is invalid", http.StatusInternalServerError)
				return
			}
			writeJSON(w, http.StatusOK, page)
			return
		}
		if !legacy {
			http.Error(w, "no repository-change diff available for ticket", http.StatusNotFound)
			return
		}
	}

	if h.provider == nil {
		http.Error(w, "diff API is not enabled", http.StatusNotFound)
		return
	}
	o, found, err := h.store.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found || o.BaseSha == "" || o.PushedSha == "" {
		http.Error(w, "no diff available for ticket", http.StatusNotFound)
		return
	}

	page, err := h.provider.Diff(o.Repo, o.BaseSha, o.PushedSha, offset, limit)
	if err != nil {
		http.Error(w, "diff unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func projectedDiffPage(change projection.RepositoryChange, offset, limit int) (gitcheck.DiffPage, error) {
	if change.SnapshotID <= 0 || change.FileCount != len(change.Files) || offset < 0 || limit <= 0 {
		return gitcheck.DiffPage{}, fmt.Errorf("invalid repository-change projection")
	}
	page := gitcheck.DiffPage{Offset: offset, Limit: limit, TotalFiles: len(change.Files), Files: []gitcheck.DiffFile{}}
	if offset >= len(change.Files) {
		return page, nil
	}
	end := offset + limit
	if end > len(change.Files) {
		end = len(change.Files)
	}
	page.HasMore = end < len(change.Files)
	page.Files = make([]gitcheck.DiffFile, 0, end-offset)
	for _, file := range change.Files[offset:end] {
		page.Files = append(page.Files, gitcheck.DiffFile{Path: file.Path, Patch: file.Patch, Truncated: file.Truncated})
	}
	return page, nil
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return def
	}
	return n
}
