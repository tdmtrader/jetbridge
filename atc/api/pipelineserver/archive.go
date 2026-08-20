package pipelineserver

import (
	"net/http"

	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/db"
)

func (s *Server) ArchivePipeline(pipelineDB db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.logger.Debug("archive-pipeline")
		err := pipelineDB.Archive()
		if err != nil {
			s.logger.Error("archive-pipeline", err)
			if errormap.Write(w, err) {
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
}
