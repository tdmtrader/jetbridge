package jobserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) RerunJobBuild(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		logger := s.logger.Session("rerun-build")

		jobName := r.FormValue(":job_name")
		buildName := r.FormValue(":build_name")

		job, found, err := pipeline.Job(jobName)
		if err != nil {
			logger.Error("failed-to-get-job", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		buildToRerun, found, err := job.Build(buildName)
		if err != nil {
			logger.Error("failed-to-get-build-to-rerun", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !found {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		if !buildToRerun.InputsReady() {
			logger.Error("build-to-rerun-has-no-inputs", errors.New("build has no inputs"))
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		acc := accessor.GetAccessor(r)
		build, err := job.RerunBuild(buildToRerun, acc.UserInfo().DisplayUserId)
		if err != nil {
			if errors.Is(err, db.ErrWorkflowRunOwnedPipeline) ||
				errors.Is(err, db.ErrAgentWorkflowResourceSourceImmutable) {
				w.WriteHeader(http.StatusConflict)
				fmt.Fprint(w, "manual reruns are disabled for durable workflow-run execution pipelines; retry the workflow run instead")
				return
			}
			logger.Error("failed-to-retrigger-build", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		err = json.NewEncoder(w).Encode(present.Build(build, nil, nil))
		if err != nil {
			logger.Error("failed-to-encode-build", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}
