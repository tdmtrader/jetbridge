package outcomes

import (
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/gitcheck"
)

// MirrorProvider opens the repo mirror and returns a windowed diff.
// Implemented by the outcome watcher's MirrorCache — one cache shared by
// the watcher component and this handler. A nil provider means the diff
// API is disabled (no --agent-outcome-git-dir, the master switch).
type MirrorProvider interface {
	Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error)
}

// DiffHandler serves GetAgentTicketDiff. Window numbers are frozen in
// §1.11.1: 50-file default window, 64 KiB per-file cap (enforced by
// gitcheck), has_more paging. It pins base_sha..pushed_sha permanently —
// unlike the external compare link, it survives the merge and works for
// private/non-GitHub remotes (both stay).
type DiffHandler struct {
	store    Store
	provider MirrorProvider
}

func NewDiffHandler(store Store, provider MirrorProvider) *DiffHandler {
	return &DiffHandler{store: store, provider: provider}
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

	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}

	page, err := h.provider.Diff(o.Repo, o.BaseSha, o.PushedSha, offset, limit)
	if err != nil {
		http.Error(w, "diff unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, page)
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
