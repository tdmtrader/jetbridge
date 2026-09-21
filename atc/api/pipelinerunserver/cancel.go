package pipelinerunserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"unicode/utf8"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) CancelPipelineRun(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The normal action-specific authorization wrapper runs before this handler
		// and before the pipeline/Run lookup. Cancellation also works after archival.
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}
		reason, err := decodeCancelReason(r)
		if err != nil {
			helpers.HandleBadRequest(w, "invalid pipeline run cancellation request")
			return
		}
		number, err := strconv.Atoi(r.URL.Query().Get(":number"))
		if err != nil || number < 1 {
			helpers.HandleBadRequest(w, "invalid pipeline run number")
			return
		}
		run, found, err := s.runFactory.GetRun(pipeline, number)
		if err != nil {
			s.logger.Error("failed-to-load-cancelled-run", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requester := accessor.GetAccessor(r).UserInfo().DisplayUserId
		outcome, err := s.runFactory.RequestRunCancellation(r.Context(), run.ID(), requester, reason)
		if err != nil {
			if errormap.Write(w, err) {
				return
			}
			s.logger.Error("failed-to-request-run-cancellation", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.logger.Info("run-cancellation", lager.Data{"run_id": run.ID(), "number": number, "team": pipeline.TeamName(), "requester": requester, "outcome": outcome})
		run, found, err = s.runFactory.GetRun(pipeline, number)
		if err != nil || !found {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		visible, err := s.pipelineRun(pipeline, run, r)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(atc.CancelPipelineRunResponse{Outcome: outcome, Run: visible}); err != nil {
			s.logger.Error("failed-to-encode-run-cancellation", err)
		}
	})
}

var errInvalidCancelRequest = errors.New("invalid pipeline run cancellation request")

func decodeCancelReason(r *http.Request) (*string, error) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return nil, errInvalidCancelRequest
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8193))
	if err != nil || len(body) > 8192 || !utf8.Valid(body) {
		return nil, errInvalidCancelRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errInvalidCancelRequest
	}
	var reason *string
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil || key != "reason" || reason != nil {
			return nil, errInvalidCancelRequest
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, errInvalidCancelRequest
		}
		reason = &value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, errInvalidCancelRequest
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errInvalidCancelRequest
	}
	return db.NormalizeRunCancellationReason(reason)
}

func canCancelRun(r *http.Request, team string) bool {
	access := accessor.GetAccessor(r)
	if !access.IsAuthenticated() {
		return false
	}
	if access.IsAdmin() || access.IsSystem() {
		return true
	}
	required := accessor.RequiredRole(r.Context(), atc.CancelPipelineRun)
	for _, role := range access.TeamRoles()[team] {
		if accessor.RoleHasRequiredRole(role, required) {
			return true
		}
	}
	return false
}
