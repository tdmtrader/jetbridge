package versionserver

import (
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) ClearResourceVersions(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("clear-resource-versions")
		resourceName := r.FormValue(":resource_name")

		resource, found, err := pipeline.Resource(resourceName)
		if err != nil {
			logger.Error("failed-to-get-resource", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !found {
			logger.Debug("resource-not-found", lager.Data{"resource-name": resourceName})
			w.WriteHeader(http.StatusNotFound)
			return
		}

		versionsDeleted, err := resource.ClearVersions()
		if err != nil {
			logger.Error("failed-to-clear-versions", err)
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}

		helpers.WriteJSONResponse(s.logger, w, atc.ClearVersionsResponse{VersionsRemoved: versionsDeleted})
	})
}
