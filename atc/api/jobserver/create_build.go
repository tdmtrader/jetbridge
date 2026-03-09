package jobserver

import (
	"encoding/json"
	"net/http"
	"strconv"

	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/tracing"
)

func (s *Server) CreateJobBuild(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		logger := s.logger.Session("create-job-build")

		jobName := r.FormValue(":job_name")

		ctx, span := tracing.StartSpan(r.Context(), "api.create-job-build", tracing.Attrs{
			"team":     pipeline.TeamName(),
			"pipeline": pipeline.Name(),
			"job":      jobName,
		})
		defer span.End()

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

		if job.DisableManualTrigger() {
			w.WriteHeader(http.StatusConflict)
			return
		}

		acc := accessor.GetAccessor(r)
		build, err := job.CreateBuild(acc.UserInfo().DisplayUserId)
		if err != nil {
			logger.Error("failed-to-create-job-build", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		span.SetAttributes(tracing.KeyValueSlice(tracing.Attrs{
			"build_id": strconv.Itoa(build.ID()),
			"build":    build.Name(),
		})...)

		resources, err := pipeline.Resources()
		if err != nil {
			logger.Error("failed-to-get-resources", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		resourceTypes, err := pipeline.ResourceTypes()
		if err != nil {
			logger.Error("failed-to-get-resource-types", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		inputs, err := job.Inputs()
		if err != nil {
			logger.Error("failed-to-get-job-inputs", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		for _, input := range inputs {
			resource, found := resources.Lookup(input.Resource)
			if found {
				version := resource.CurrentPinnedVersion()
				_, _, err := s.checkFactory.TryCreateCheck(
					lagerctx.NewContext(ctx, logger),
					resource,
					resourceTypes,
					version,
					true,
					true,
					true,
				)
				if err != nil {
					logger.Error("failed-to-create-check", err)
				}
			}
		}

		err = json.NewEncoder(w).Encode(present.Build(build, nil, nil))
		if err != nil {
			logger.Error("failed-to-encode-build", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}
