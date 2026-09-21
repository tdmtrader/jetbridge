package pipelinerunserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

type ResultReader interface {
	Read(context.Context, int, string) (*hangar.CapturedTree, error)
}

func (s *Server) SetResultReader(reader ResultReader) { s.services.Results = reader }

func (s *Server) GetPipelineRunResult(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		access := accessor.GetAccessor(r)
		if !access.IsAuthenticated() {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !access.IsAuthorized(pipeline.TeamName()) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}
		number, err := strconv.Atoi(r.FormValue(":number"))
		name := r.FormValue(":result_name")
		if err != nil || number < 1 || output.OutputName(name).Validate() != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		run, found, err := s.runFactory.GetRun(pipeline, number)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if run.Status() == atc.RunStatusRunning {
			w.WriteHeader(http.StatusConflict)
			return
		}
		if s.services.Results == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// The slot is held from the node read until the response is written:
		// the spooled archive occupies web scratch for all of it. Refuse
		// rather than queue, so a burst cannot pile up behind the bound.
		select {
		case s.resultSlots <- struct{}{}:
			defer func() { <-s.resultSlots }()
		default:
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		tree, err := s.services.Results.Read(r.Context(), run.ID(), name)
		if err != nil {
			switch {
			case errors.Is(err, output.ErrNotFound):
				w.WriteHeader(http.StatusNotFound)
			case errors.Is(err, atc.ErrRunResultPending):
				w.WriteHeader(http.StatusConflict)
			default:
				w.WriteHeader(http.StatusServiceUnavailable)
			}
			return
		}
		defer tree.Close()
		file, err := os.Open(tree.ArchivePath)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer file.Close()
		// The node read is already complete. Bound only the external client
		// transfer here, independently of the configured materialization budget.
		deadline := time.Now().Add(2 * time.Minute)
		if err = http.NewResponseController(w).SetWriteDeadline(deadline); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-tar")
		w.Header().Set("Content-Length", fmt.Sprint(tree.ByteSize))
		_, _ = io.Copy(w, file)
	})
}
