package transcriptserver

import (
	"io"
	"net/http"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
)

// GetTranscript serves GET
// /api/v1/agent/tickets/:ticket_id/runs/:build_id/transcript — the raw
// tool-call transcript (ndjson) the runner captured for a ticket's run
// (ticket #43). 404 when no transcript row exists for that ticket+build.
func (s *Server) GetTranscript(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.Session("get-agent-ticket-run-transcript")

	ticketID, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || ticketID <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return
	}
	buildID, err := strconv.Atoi(r.FormValue(":build_id"))
	if err != nil || buildID <= 0 {
		http.Error(w, "invalid build_id", http.StatusBadRequest)
		return
	}

	if s.store == nil {
		http.Error(w, "transcript API is not enabled", http.StatusNotFound)
		return
	}

	ndjson, found, err := s.store.Get(ticketID, buildID)
	if err != nil {
		logger.Error("failed-to-get-transcript", err, lager.Data{"ticket_id": ticketID, "build_id": buildID})
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found {
		http.Error(w, "no transcript available for run", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, ndjson)
}
