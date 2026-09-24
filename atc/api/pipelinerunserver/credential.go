package pipelinerunserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar/output"
)

func (s *Server) GetPipelineRunCredentialSession(pipeline db.Pipeline) http.Handler {
	return s.credentialSession(pipeline, false)
}

func (s *Server) HandoffPipelineRunCredentials(pipeline db.Pipeline) http.Handler {
	return s.credentialSession(pipeline, true)
}

func (s *Server) credentialSession(pipeline db.Pipeline, deliver bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		access, claims := accessor.GetAccessor(r), accessor.VerifiedClaims(r)
		if !access.IsAuthenticated() || claims == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !access.IsAuthorized(pipeline.TeamName()) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if s.services.Admitter == nil || s.services.Epoch <= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}
		number, err := strconv.Atoi(r.URL.Query().Get(":number"))
		result := r.URL.Query().Get(":result_name")
		if err != nil || number < 1 || result == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ref := runs.TemplateRef{Team: pipeline.TeamName(), Pipeline: pipeline.PipelineRef()}
		principal := runs.Principal{Claims: claims}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		var state atc.RunCredentialSession
		if deliver {
			if !atc.PipelineRunsActivated() {
				errormap.Write(w, atc.ErrPipelineRunCreationDisabled)
				return
			}
			// Credentials are JSON and nothing else. Refusing every other
			// media type before the body is read keeps a form-encoded body
			// from ever being parsed as one, here or upstream.
			if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/json" {
				w.WriteHeader(http.StatusUnsupportedMediaType)
				return
			}
			deadline, _ := ctx.Deadline()
			controller := http.NewResponseController(w)
			if err := controller.SetReadDeadline(deadline); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			defer controller.SetReadDeadline(time.Time{})
			body := http.MaxBytesReader(w, r.Body, 65537)
			state, err = s.services.Admitter.HandoffCredentials(ctx, ref, principal, number, result, s.services.Epoch, body)
		} else {
			state, err = s.services.Admitter.InspectCredentialHandoff(ctx, ref, principal, number, result, s.services.Epoch)
		}
		if err != nil {
			switch {
			case errors.Is(err, runs.ErrUnauthorized):
				w.WriteHeader(http.StatusForbidden)
			case errors.Is(err, sql.ErrNoRows), errors.Is(err, runs.ErrTemplateNotFound):
				w.WriteHeader(http.StatusNotFound)
			case errors.Is(err, runs.ErrCredentialInput), errors.Is(err, output.ErrInvalidIdentity):
				w.WriteHeader(http.StatusBadRequest)
			case errors.Is(err, output.ErrConflict):
				w.WriteHeader(http.StatusConflict)
			default:
				if !errormap.Write(w, err) {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	})
}
