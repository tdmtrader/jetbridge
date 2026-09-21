package pipelinerunserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

func (s *Server) UploadPipelineRunInput(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		access := accessor.GetAccessor(r)
		claims := accessor.VerifiedClaims(r)
		if !access.IsAuthenticated() || claims == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !access.IsAuthorized(pipeline.TeamName()) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if !atc.EnablePipelineRunCreation {
			errormap.Write(w, atc.ErrPipelineRunCreationDisabled)
			return
		}
		if s.services.Admitter == nil || s.services.Epoch <= 0 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(deadline); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer controller.SetReadDeadline(time.Time{})
		limit, _ := hangar.CanonicalArchiveByteLimit(output.MaxInputContentBytes, hangar.DefaultMaxTreeEntries)
		body := http.MaxBytesReader(w, r.Body, limit)
		defer body.Close()
		source, err := s.services.Admitter.UploadInput(ctx, runs.TemplateRef{Team: pipeline.TeamName(), Pipeline: pipeline.PipelineRef()}, runs.Principal{Claims: claims}, r.URL.Query().Get(":input_name"), s.services.Epoch, body)
		if err != nil {
			switch {
			case errors.Is(err, runs.ErrUnauthorized):
				w.WriteHeader(http.StatusForbidden)
			case errors.Is(err, atc.ErrInvalidRunInputs), errors.Is(err, output.ErrIncomplete), errors.Is(err, output.ErrLimitExceeded):
				w.WriteHeader(http.StatusBadRequest)
			case errors.Is(err, runs.ErrNotATemplate), errors.Is(err, runs.ErrTemplatePaused), errors.Is(err, runs.ErrTemplateArchived):
				w.WriteHeader(http.StatusConflict)
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(source)
	})
}
