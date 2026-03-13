package infoserver

import (
	"encoding/json"
	"net/http"
)

type HealthStatus struct {
	Healthy  bool   `json:"healthy"`
	DB       string `json:"db"`
	Workers  string `json:"workers"`
	DBError  string `json:"db_error,omitempty"`
}

func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	logger := s.logger.Session("health")

	status := HealthStatus{Healthy: true}

	// Check DB connectivity
	if s.dbPinger != nil {
		if err := s.dbPinger.Ping(); err != nil {
			status.Healthy = false
			status.DB = "unhealthy"
			status.DBError = err.Error()
			logger.Error("db-ping-failed", err)
		} else {
			status.DB = "ok"
		}
	} else {
		status.DB = "not-configured"
	}

	// Check worker availability
	if s.workerFactory != nil {
		workers, err := s.workerFactory.Workers()
		if err != nil {
			status.Healthy = false
			status.Workers = "error"
			logger.Error("worker-check-failed", err)
		} else if len(workers) == 0 {
			status.Healthy = false
			status.Workers = "none"
		} else {
			status.Workers = "ok"
		}
	} else {
		status.Workers = "not-configured"
	}

	w.Header().Set("Content-Type", "application/json")
	if !status.Healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	if err := json.NewEncoder(w).Encode(status); err != nil {
		logger.Error("failed-to-encode-health", err)
	}
}
