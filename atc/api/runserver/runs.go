package runserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) CreateRun(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("create-pipeline-run", lager.Data{"pipeline": pipeline.Name()})
		w.Header().Set("Content-Type", "application/json")

		if !pipeline.Template() || pipeline.InstanceVars() != nil {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `only template pipelines can be run; set "template: true"`)
			return
		}

		var req atc.CreatePipelineRunRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil && !errors.Is(err, io.EOF) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, "malformed request body")
			return
		}

		validated, err := atc.ValidateRunParams(pipeline.ParamsSchema(), req.Params)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, err.Error())
			return
		}

		acc := accessor.GetAccessor(r)
		// pass-through: CreateRun itself triggers entry jobs AND enqueues
		// the frozen check set (shared-contracts §7.1 items 2/8; F27,
		// 2026-07-09 — previously the enqueue lived here, which left
		// factory-created runs without their frozen checks)
		run, err := s.runFactory.CreateRun(pipeline.ID(), validated, acc.UserInfo().DisplayUserId)
		if err != nil {
			if errors.Is(err, db.ErrWorkflowRunOwnedPipeline) {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, err.Error())
				return
			}
			logger.Error("failed-to-create-run", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusCreated)
		err = json.NewEncoder(w).Encode(present.PipelineRun(run))
		if err != nil {
			logger.Error("failed-to-encode-run", err)
		}
	})
}

func (s *Server) ListRuns(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("list-pipeline-runs")
		w.Header().Set("Content-Type", "application/json")

		limit := 100
		if l := r.URL.Query().Get("limit"); l != "" {
			parsed, err := strconv.Atoi(l)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			limit = parsed
		}

		runs, err := s.runFactory.ListRuns(pipeline.ID(), limit)
		if err != nil {
			logger.Error("failed-to-list-runs", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		err = json.NewEncoder(w).Encode(present.PipelineRuns(runs))
		if err != nil {
			logger.Error("failed-to-encode-runs", err)
		}
	})
}

func (s *Server) GetRun(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("get-pipeline-run")
		w.Header().Set("Content-Type", "application/json")

		number, err := strconv.Atoi(r.FormValue(":run_number"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		run, found, err := s.runFactory.GetRun(pipeline.ID(), number)
		if err != nil {
			logger.Error("failed-to-get-run", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		err = json.NewEncoder(w).Encode(present.PipelineRun(run))
		if err != nil {
			logger.Error("failed-to-encode-run", err)
		}
	})
}
