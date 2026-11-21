package versionserver

import (
	"net/http"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
)

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

		response := make([]atc.DeprecatedScope, 0, len(deprecated))
		for _, scope := range deprecated {
			response = append(response, atc.DeprecatedScope{
				ID:           scope.ID,
				DeprecatedAt: formatScopeTime(scope.DeprecatedAt),
				ConfigID:     scope.ConfigID,
				Versions:     scope.Versions,
				LastChecked:  formatScopeTime(scope.LastCheckEnd),
			})
		}

		helpers.WriteJSONResponse(s.logger, w, response)
	})
}

// formatScopeTime is empty for a time never set: a live history has no
// deprecation time, and a scope never checked has the epoch as its last check.
func formatScopeTime(t time.Time) string {
	if t.IsZero() || t.Unix() <= 0 {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05Z")
}
