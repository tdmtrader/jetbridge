package jobserver

import (
	"fmt"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
	"github.com/google/jsonapi"
)

func (s *Server) ClearTaskCache(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("clear-task-cache")
		jobName := r.FormValue(":job_name")
		stepName := r.FormValue(":step_name")
		cachePath := r.FormValue(atc.ClearTaskCacheQueryPath)

		job, found, err := pipeline.Job(jobName)
		if err != nil {
			logger.Error("failed-to-get-job", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !found {
			logger.Debug("could-not-find-job", lager.Data{
				"jobName":   jobName,
				"stepName":  stepName,
				"cachePath": cachePath,
			})
			w.Header().Set("Content-Type", jsonapi.MediaType)
			w.WriteHeader(http.StatusNotFound)
			_ = jsonapi.MarshalErrors(w, []*jsonapi.ErrorObject{{
				Title:  "Job Not Found Error",
				Detail: fmt.Sprintf("Job with name '%s' not found.", jobName),
				Status: "404",
			}})
			return
		}

		rowsDeleted, err := job.ClearTaskCache(stepName, cachePath)

		if err != nil {
			logger.Error("failed-to-clear-task-cache", err)
			if errormap.Write(w, err) {
				return
			}
			w.Header().Set("Content-Type", jsonapi.MediaType)
			w.WriteHeader(http.StatusInternalServerError)
			_ = jsonapi.MarshalErrors(w, []*jsonapi.ErrorObject{{
				Title:  "Clear Task Cache Error",
				Detail: err.Error(),
				Status: "500",
			}})
			return
		}

		helpers.WriteJSONResponse(s.logger, w, atc.ClearTaskCacheResponse{CachesRemoved: rowsDeleted})
	})
}
