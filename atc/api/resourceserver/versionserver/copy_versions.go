package versionserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/helpers"
	"github.com/concourse/concourse/atc/db"
)

type CopyVersionsRequest struct {
	FromScopeID int `json:"from_scope_id"`
}

func (s *Server) CopyResourceVersions(pipeline db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("copy-resource-versions")
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

		var req CopyVersionsRequest
		err = json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			logger.Error("failed-to-decode-request", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		if req.FromScopeID == 0 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"from_scope_id is required"}`))
			return
		}

		// Validate the source scope belongs to this resource
		deprecated, err := resource.DeprecatedScopes()
		if err != nil {
			logger.Error("failed-to-get-deprecated-scopes", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		var validScope bool
		for _, scope := range deprecated {
			if scope.ID == req.FromScopeID {
				validScope = true
				break
			}
		}

		if !validScope {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"source scope not found or does not belong to this resource"}`))
			return
		}

		copied, err := resource.CopyVersionsFromScope(req.FromScopeID)
		if errors.Is(err, db.ErrVersionHistoryNotFound) {
			// Listed above, but collected (or not the resource's team's) by
			// the time the copy locked it.
			logger.Info("source-scope-gone", lager.Data{"from-scope-id": req.FromScopeID})
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"source scope not found or does not belong to this resource"}`))
			return
		}
		if errors.Is(err, db.ErrCopyIntoSharedScope) {
			logger.Info("refused-copy-into-shared-scope", lager.Data{"from-scope-id": req.FromScopeID, "reason": err.Error()})
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		if err != nil {
			logger.Error("failed-to-copy-versions", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		helpers.WriteJSONResponse(s.logger, w, atc.CopyVersionsResponse{VersionsCopied: copied})
	})
}
