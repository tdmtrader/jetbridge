package buildserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
	"github.com/jackc/pgx/v5/pgconn"
)

// This finite view shares the BuildEvents route and private-job-output check.
// It never constructs an SSE handler, listener, polling goroutine or Flusher.
func (s *Server) eventPage(w http.ResponseWriter, r *http.Request, build db.BuildForAPI) {
	request := atc.BuildEventPageRequest{Cursor: r.URL.Query().Get("cursor")}
	if raw := r.URL.Query().Get("max_bytes"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 65536 {
			helpers.HandleBadRequest(w, "max_bytes must be in 1..65536")
			return
		}
		request.MaxBytes = value
	}
	page, err := build.EventPage(r.Context(), request)
	if err != nil {
		code, message, status := "TEMPORARY_OUTPUT_FAILURE", "build output is temporarily unavailable", http.StatusServiceUnavailable
		var pgErr *pgconn.PgError
		switch {
		case errors.Is(err, db.ErrBuildEventCursor):
			code, message, status = "INVALID_CURSOR", err.Error(), http.StatusBadRequest
		case errors.Is(err, db.ErrBuildEventStreamChanged):
			code, message, status = "STREAM_CHANGED", err.Error(), http.StatusConflict
		case errors.Is(err, db.ErrBuildEventsReaped):
			code, message, status = "OUTPUT_RETAINED_AWAY", err.Error(), http.StatusGone
		case errors.Is(err, db.ErrBuildEventsUnavailable), errors.As(err, &pgErr) && pgErr.Code == "42P01":
			code, message, status = "OUTPUT_UNAVAILABLE", "build or event storage unavailable", http.StatusNotFound
		case errors.Is(err, db.ErrBuildEventStoredTooLarge):
			code, message, status = "STORED_EVENT_TOO_LARGE", err.Error(), http.StatusRequestEntityTooLarge
		case errors.Is(err, db.ErrBuildEventTooLarge):
			code, message, status = "EVENT_TOO_LARGE", err.Error(), http.StatusRequestEntityTooLarge
		case errors.Is(err, db.ErrBuildEventPageTooSmall):
			code, message, status = "PAGE_TOO_SMALL", err.Error(), http.StatusBadRequest
		}
		// Each sentinel's text already opens with the code the envelope carries.
		// Sending both makes every client that prefixes the code -- the MCP
		// adapter does -- print it twice: "INVALID_CURSOR: INVALID_CURSOR: ...".
		message = strings.TrimPrefix(message, code+": ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		helpers.WriteErrorResponse(w, atc.ErrorResponse{Code: code, Errors: []string{message}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}
