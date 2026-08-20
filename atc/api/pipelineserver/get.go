package pipelineserver

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc/api/accessor"
	"github.com/concourse/concourse/atc/api/present"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) GetPipeline(pipeline db.Pipeline) http.Handler {
	logger := s.logger.Session("get-pipeline")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		acc := accessor.GetAccessor(r)
		err := json.NewEncoder(w).Encode(present.Pipeline(pipeline, pipelineOptions(acc, pipeline)))
		if err != nil {
			logger.Error("failed-to-encode-pipeline", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}

func pipelineOptions(acc accessor.Access, pipeline db.Pipeline) present.PipelineOptions {
	return present.PipelineOptions{
		AuthorizedForParams: acc.IsAuthorized(pipeline.TeamName()),
		CanCreateRun:        canCreatePipelineRun(acc, pipeline.TeamName()),
	}
}

func canCreatePipelineRun(acc accessor.Access, teamName string) bool {
	if acc.IsAdmin() {
		return true
	}
	for _, role := range acc.TeamRoles()[teamName] {
		if role == accessor.MemberRole || role == accessor.OwnerRole {
			return true
		}
	}
	return false
}
