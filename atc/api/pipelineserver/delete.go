package pipelineserver

import (
	"encoding/json"
	"net/http"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/errormap"
	"github.com/concourse/concourse/atc/db"
)

// DeletePipeline destroys a pipeline. With purge_run_history=true it destroys
// a template together with every one of its Runs and their durable history,
// and answers how many Runs went; without it a template with Run history is
// refused, as it always was.
func (s *Server) DeletePipeline(pipelineDB db.Pipeline) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := s.logger.Session("destroying-pipeline", lager.Data{
			"name": pipelineDB.Name(),
		})

		var purge bool
		switch r.URL.Query().Get(atc.DestroyPipelineQueryPurgeRunHistory) {
		case "", "false":
		case "true":
			purge = true
		default:
			// A typo must not be read as either answer to a destructive
			// question.
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		logger.Info("start", lager.Data{"purge-run-history": purge})

		var purged int
		var err error
		if purge {
			purged, err = pipelineDB.DestroyWithRunHistory()
		} else {
			err = pipelineDB.Destroy()
		}
		if err != nil {
			logger.Error("failed", err)
			if errormap.Write(w, err) {
				return
			}

			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		if !purge {
			logger.Info("done")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		logger.Info("done", lager.Data{"purged-runs": purged})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err = json.NewEncoder(w).Encode(atc.DestroyPipelineResponse{PurgedRuns: purged}); err != nil {
			logger.Error("failed-to-encode-response", err)
		}
	})
}
