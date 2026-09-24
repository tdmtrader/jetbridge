package pipelinerunserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
)

func (s *Server) CreatePipelineRunV2(pipeline db.Pipeline) http.Handler {
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
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(deadline); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer controller.SetReadDeadline(time.Time{})
		body := http.MaxBytesReader(w, r.Body, 1<<20)
		defer body.Close()
		decoder := json.NewDecoder(body)
		decoder.DisallowUnknownFields()
		var request atc.CreatePipelineRunV2Request
		if err := decoder.Decode(&request); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		tx, err := s.services.Admitter.Begin(ctx)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		defer tx.Rollback()
		admitted, replayed, err := s.services.Admitter.AdmitVersionedRun(ctx, tx, runs.Admission{
			Template:  runs.TemplateRef{Team: pipeline.TeamName(), Pipeline: pipeline.PipelineRef()},
			Principal: runs.Principal{Claims: claims}, ContractKey: request.InvocationKey,
			Params: request.Vars, Inputs: request.Inputs,
			CausedByRun: request.CausedByRun, Correlation: request.Correlation,
		}, s.services.Epoch)
		if err != nil {
			writeVersionedRefusal(w, err)
			return
		}
		if err := db.HangarCommitError(tx.Commit()); err != nil {
			writeVersionedRefusal(w, err)
			return
		}
		// The durable Run is the replay authority even if the response or this
		// best-effort scheduler wakeup is lost. A new request never re-admits it.
		run, found, err := s.runFactory.GetRun(pipeline, admitted.Number)
		if err != nil || !found || run.ID() != admitted.ID {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = s.runFactory.AfterRunCreated(ctx, db.RunCreation{Run: run, Replayed: replayed})
		status := http.StatusCreated
		if replayed {
			status = http.StatusOK
		}
		s.writeRun(w, pipeline, run, r, status)
	})
}

func writeVersionedRefusal(w http.ResponseWriter, err error) {
	var invalid runs.InvalidParamsError
	switch {
	case errors.Is(err, runs.ErrUnauthorized):
		w.WriteHeader(http.StatusForbidden)
	case errors.Is(err, runs.ErrTemplateNotFound):
		w.WriteHeader(http.StatusNotFound)
	case errors.Is(err, runs.ErrInvalidInvocationKey), errors.Is(err, runs.ErrUnsupportedInvocation),
		errors.Is(err, runs.ErrInvalidCorrelation), errors.Is(err, runs.ErrRunCauseUnavailable), errors.Is(err, atc.ErrInvalidRunInputs), errors.Is(err, atc.ErrRunInputUnavailable), errors.As(err, &invalid):
		w.WriteHeader(http.StatusBadRequest)
	case errors.Is(err, runs.ErrInvocationConflict), errors.Is(err, runs.ErrTemplatePaused), errors.Is(err, runs.ErrTemplateArchived), errors.Is(err, runs.ErrNotATemplate), errors.Is(err, runs.ErrTemplateInstanced):
		w.WriteHeader(http.StatusConflict)
	default:
		if !errormap.Write(w, err) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}
}
