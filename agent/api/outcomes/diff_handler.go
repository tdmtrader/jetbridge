package outcomes

import (
	"net/http"
	"strconv"

	"github.com/concourse/concourse/agent/deliverydiff"
	"github.com/concourse/concourse/agent/gitcheck"
)

// MirrorProvider is the historical compatibility fallback for deliveries
// captured before Postgres-backed diffs were introduced.
type MirrorProvider interface {
	Diff(repo, base, pushed string, offset, limit int) (gitcheck.DiffPage, error)
}

// DiffHandler serves GetAgentTicketDiff. Window numbers are frozen in
// §1.11.1: 50-file default window, 64 KiB per-file cap (enforced by
// gitcheck), has_more paging. It pins base_sha..pushed_sha permanently —
// unlike the external compare link, it survives the merge and works for
// private/non-GitHub remotes (both stay).
type DiffHandler struct {
	outcomes Store
	diffs    deliverydiff.Store
	fallback MirrorProvider
}

func NewDiffHandler(outcomes Store, diffs deliverydiff.Store, fallback MirrorProvider) *DiffHandler {
	return &DiffHandler{outcomes: outcomes, diffs: diffs, fallback: fallback}
}

// GetDiff handles GET /api/v1/agent/tickets/:ticket_id/diff. New deliveries
// are served from immutable Postgres evidence; the Git mirror is consulted
// only for historical deliveries that have no stored attempt.
func (h *DiffHandler) GetDiff(w http.ResponseWriter, r *http.Request) {
	id, ok := ticketID(w, r)
	if !ok {
		return
	}

	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	limit := atoiDefault(r.URL.Query().Get("limit"), 50)
	if limit > 200 {
		limit = 200
	}
	if h.diffs != nil {
		stored, found, err := h.diffs.GetLatest(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if found {
			writeJSON(w, http.StatusOK, stored.Page(offset, limit))
			return
		}
	}

	if h.fallback == nil {
		http.Error(w, "no diff available for ticket", http.StatusNotFound)
		return
	}
	o, found, err := h.outcomes.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found || o.BaseSha == "" || o.PushedSha == "" {
		http.Error(w, "no diff available for ticket", http.StatusNotFound)
		return
	}

	page, err := h.fallback.Diff(o.Repo, o.BaseSha, o.PushedSha, offset, limit)
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
