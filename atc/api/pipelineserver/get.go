package pipelineserver

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) GetPipeline(pipeline db.Pipeline) http.Handler {
	logger := s.logger.Session("get-pipeline")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		acc := accessor.GetAccessor(r)
		err := json.NewEncoder(w).Encode(present.Pipeline(pipeline, pipelineOptions(r, acc, pipeline)))
		if err != nil {
			logger.Error("failed-to-encode-pipeline", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func pipelineOptions(r *http.Request, acc accessor.Access, pipeline db.Pipeline) present.PipelineOptions {
	return present.PipelineOptions{
		AuthorizedForParams: acc.IsAuthorized(pipeline.TeamName()),
		CanCreateRun:        canCreatePipelineRun(acc, pipeline.TeamName(), accessor.RequiredRole(r.Context(), atc.CreatePipelineRun)),
	}
}

func canCreatePipelineRun(acc accessor.Access, teamName string, requiredRole string) bool {
	// The operator gate narrows this field and never widens it: with creation
	// held, no role can create a run, so no caller may be told it can. One
	// edit, three payloads -- this is the only place the field is computed.
	if !atc.EnablePipelineRunCreation {
		return false
	}
	if !acc.IsAuthenticated() {
		return false
	}
	if acc.IsAdmin() {
		return true
	}
	for _, role := range acc.TeamRoles()[teamName] {
		if accessor.RoleHasRequiredRole(role, requiredRole) {
			return true
		}
	}
	return false
}
