package pipelinerunserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
)

const defaultPipelineRunLimit = 50

type Server struct {
	logger      lager.Logger
	runFactory  db.PipelineRunFactory
	externalURL string
}

func NewServer(logger lager.Logger, runFactory db.PipelineRunFactory, externalURL string) *Server {
	return &Server{logger: logger, runFactory: runFactory, externalURL: externalURL}
}

// presentRun is the single place the run presentation options are decided, so
// the per-run detail path and the batched listing path cannot drift apart. A
// nil instance is the reclaimed state and the only reclaimed state.
func (s *Server) presentRun(pipeline db.Pipeline, run db.PipelineRun, instance db.Pipeline, r *http.Request) atc.PipelineRun {
	access := accessor.GetAccessor(r)
	return present.PipelineRun(run, instance, present.PipelineRunOptions{
		AuthorizedForParams: access.IsAuthorized(pipeline.TeamName()),
		CanEnterPayload:     instance != nil && (instance.Public() || access.IsAuthorized(instance.TeamName())),
	})
}

func (s *Server) pipelineRun(pipeline db.Pipeline, run db.PipelineRun, r *http.Request) (atc.PipelineRun, error) {
	instance, found, err := s.runFactory.InstancePipeline(run)
	if err != nil {
		return atc.PipelineRun{}, err
	}
	if !found {
		instance = nil
	}
	return s.presentRun(pipeline, run, instance, r), nil
}

func (s *Server) writeRun(w http.ResponseWriter, pipeline db.Pipeline, run db.PipelineRun, r *http.Request, status int) {
	presentable, err := s.pipelineRun(pipeline, run, r)
	if err != nil {
		s.logger.Error("failed-to-load-pipeline-run-payload", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(presentable); err != nil {
		s.logger.Error("failed-to-encode-pipeline-run", err)
	}
}

func (s *Server) CreatePipelineRun(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}

		var request atc.CreatePipelineRunRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			helpers.HandleBadRequest(w, "invalid pipeline run request")
			return
		}

		access := accessor.GetAccessor(r)
		creation, err := s.runFactory.CreateRun(r.Context(), pipeline, db.RunParams{Vars: atc.RunParams(request.Vars)}, access.UserInfo().DisplayUserId)
		if err != nil {
			s.logger.Error("failed-to-create-pipeline-run", err)
			if errormap.Write(w, err) {
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		s.writeRun(w, pipeline, creation.Run, r, http.StatusCreated)
	})
}

func (s *Server) ListPipelineRuns(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}

		page, err := pipelineRunPage(r)
		if err != nil {
			helpers.HandleBadRequest(w, err.Error())
			return
		}

		runs, pagination, err := s.runFactory.Runs(pipeline, page)
		if err != nil {
			s.logger.Error("failed-to-list-pipeline-runs", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// One query for the whole page. Resolving payloads run-by-run made this
		// unauthenticated-reachable route issue up to atc.PaginationAPIMaxLimit
		// round trips per request.
		payloads, err := s.runFactory.InstancePipelines(runs)
		if err != nil {
			s.logger.Error("failed-to-load-pipeline-run-payload", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		presented := make([]atc.PipelineRun, len(runs))
		for i, run := range runs {
			// A run with no payload row is reclaimed: the missing map entry is
			// the nil db.Pipeline the presenter reads as such.
			presented[i] = s.presentRun(pipeline, run, payloads[run.ID()], r)
		}

		if pagination.Older != nil {
			s.addNextLink(w, pipeline, *pagination.Older)
		}
		if pagination.Newer != nil {
			s.addPreviousLink(w, pipeline, *pagination.Newer)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(presented); err != nil {
			s.logger.Error("failed-to-encode-pipeline-runs", err)
		}
	})
}

func (s *Server) GetPipelineRun(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rejectInstancedPipelineRun(w, pipeline) {
			return
		}

		number, err := strconv.Atoi(r.FormValue(":number"))
		if err != nil || number < 1 {
			helpers.HandleBadRequest(w, "invalid pipeline run number")
			return
		}

		run, found, err := s.runFactory.GetRun(pipeline, number)
		if err != nil {
			s.logger.Error("failed-to-get-pipeline-run", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		s.writeRun(w, pipeline, run, r, http.StatusOK)
	})
}

func rejectInstancedPipelineRun(w http.ResponseWriter, pipeline db.Pipeline) bool {
	if pipeline.InstanceVars() == nil {
		return false
	}
	errormap.Write(w, db.ErrPipelineRunInstanced)
	return true
}

func pipelineRunPage(r *http.Request) (db.Page, error) {
	page := db.Page{Limit: defaultPipelineRunLimit}
	for key, destination := range map[string]**int{
		atc.PaginationQueryFrom: &page.From,
		atc.PaginationQueryTo:   &page.To,
	} {
		value := r.FormValue(key)
		if value == "" {
			continue
		}
		number, err := strconv.Atoi(value)
		if err != nil || number < 1 {
			return db.Page{}, fmt.Errorf("invalid %s pagination value", key)
		}
		*destination = db.NewIntPtr(number)
	}

	if value := r.FormValue(atc.PaginationQueryLimit); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 {
			return db.Page{}, fmt.Errorf("invalid limit pagination value")
		}
		// This route is reachable by an unauthenticated viewer on an exposed
		// template, so an unbounded caller-supplied limit is an unauthenticated
		// amplifier: it sizes the response, the payload batch's IN list, and the
		// rows scanned for it. A well-formed but absurd limit clamps rather than
		// 400s, so existing scripted callers keep working; the Link headers then
		// echo the clamped value.
		if limit > atc.PaginationAPIMaxLimit {
			limit = atc.PaginationAPIMaxLimit
		}
		page.Limit = limit
	}
	if page.From != nil && page.To != nil && *page.From > *page.To {
		return db.Page{}, fmt.Errorf("invalid range boundaries")
	}
	return page, nil
}

func (s *Server) addNextLink(w http.ResponseWriter, pipeline db.Pipeline, page db.Page) {
	w.Header().Add("Link", fmt.Sprintf(
		`<%s/api/v1/teams/%s/pipelines/%s/runs?%s=%d&%s=%d>; rel="%s"`,
		s.externalURL, pipeline.TeamName(), pipeline.Name(), atc.PaginationQueryTo, *page.To, atc.PaginationQueryLimit, page.Limit, atc.LinkRelNext,
	))
}

func (s *Server) addPreviousLink(w http.ResponseWriter, pipeline db.Pipeline, page db.Page) {
	w.Header().Add("Link", fmt.Sprintf(
		`<%s/api/v1/teams/%s/pipelines/%s/runs?%s=%d&%s=%d>; rel="%s"`,
		s.externalURL, pipeline.TeamName(), pipeline.Name(), atc.PaginationQueryFrom, *page.From, atc.PaginationQueryLimit, page.Limit, atc.LinkRelPrevious,
	))
}
