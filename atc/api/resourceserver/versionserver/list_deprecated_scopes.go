package versionserver

import (
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
)

type DeprecatedScopeResponse struct {
	ID           int    `json:"id"`
	DeprecatedAt string `json:"deprecated_at"`
	ConfigID     int    `json:"config_id"`
}

func (s *Server) ListDeprecatedScopes(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("list-deprecated-scopes")
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

		deprecated, err := resource.DeprecatedScopes()
		if err != nil {
			logger.Error("failed-to-get-deprecated-scopes", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		var response []DeprecatedScopeResponse
		for _, scope := range deprecated {
			response = append(response, DeprecatedScopeResponse{
				ID:           scope.ID,
				DeprecatedAt: scope.DeprecatedAt.Format("2006-01-02T15:04:05Z"),
				ConfigID:     scope.ConfigID,
			})
		}

		if response == nil {
			response = []DeprecatedScopeResponse{}
		}

		s.writeJSONResponse(w, response)
	})
}
