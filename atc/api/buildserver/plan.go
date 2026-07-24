package buildserver

import (
	"encoding/json"
	"net/http"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/legacyplan"
)

func (s *Server) GetBuildPlan(build db.BuildForAPI) http.Handler {
	hLog := s.logger.Session("get-build-plan")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !build.HasPlan() {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		plan := build.PublicPlan()
		containsHarvest := false
		var err error
		if plan != nil {
			containsHarvest, err = legacyplan.ContainsHarvest(*plan)
			if err != nil {
				hLog.Error("failed-to-decode-historical-public-build-plan", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}
		running := build.IsRunning()
		if containsHarvest && running {
			http.Error(w, legacyplan.ErrActiveHarvestPlan.Error(), http.StatusConflict)
			return
		}
		if !running {
			plan, err = legacyplan.DecodeCompletedPublic(plan)
			if err != nil {
				hLog.Error("failed-to-decode-historical-public-build-plan", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
		}

		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(atc.PublicBuildPlan{
			Schema: build.Schema(),
			Plan:   plan,
		})
		if err != nil {
			hLog.Error("failed-to-encode-public-build-plan", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	})
}
