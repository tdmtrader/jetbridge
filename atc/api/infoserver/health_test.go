package infoserver_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/infoserver"
	"github.com/concourse/concourse/atc/db"
)

type fakePinger struct {
	err error
}

func (f *fakePinger) Ping() error { return f.err }

type fakeWorkerFactory struct {
	workers []db.Worker
	err     error
}

func (f *fakeWorkerFactory) Workers() ([]db.Worker, error) {
	return f.workers, f.err
}

// Stub the rest of the WorkerFactory interface — only Workers() is used by health check.
func (f *fakeWorkerFactory) GetWorker(string) (db.Worker, bool, error) { return nil, false, nil }
func (f *fakeWorkerFactory) SaveWorker(atc.Worker, time.Duration) (db.Worker, error) {
	return nil, nil
}
func (f *fakeWorkerFactory) VisibleWorkers([]string) ([]db.Worker, error) { return nil, nil }
func (f *fakeWorkerFactory) FindWorkersForContainerByOwner(db.ContainerOwner) ([]db.Worker, error) {
	return nil, nil
}
func (f *fakeWorkerFactory) BuildContainersCountPerWorker() (map[string]int, error) {
	return nil, nil
}

func TestHealth(t *testing.T) {
	logger := lagertest.NewTestLogger("health-test")

	tests := []struct {
		name           string
		pingErr        error
		workers        []db.Worker
		workerErr      error
		expectedStatus int
		expectedDB     string
		expectedWkrs   string
	}{
		{
			name:           "healthy with DB and workers",
			pingErr:        nil,
			workers:        []db.Worker{nil}, // non-empty slice
			workerErr:      nil,
			expectedStatus: http.StatusOK,
			expectedDB:     "ok",
			expectedWkrs:   "ok",
		},
		{
			name:           "unhealthy when DB is down",
			pingErr:        errors.New("connection refused"),
			workers:        []db.Worker{nil},
			workerErr:      nil,
			expectedStatus: http.StatusServiceUnavailable,
			expectedDB:     "unhealthy",
			expectedWkrs:   "ok",
		},
		{
			name:           "unhealthy when no workers",
			pingErr:        nil,
			workers:        []db.Worker{},
			workerErr:      nil,
			expectedStatus: http.StatusServiceUnavailable,
			expectedDB:     "ok",
			expectedWkrs:   "none",
		},
		{
			name:           "unhealthy when worker factory errors",
			pingErr:        nil,
			workers:        nil,
			workerErr:      errors.New("cache error"),
			expectedStatus: http.StatusServiceUnavailable,
			expectedDB:     "ok",
			expectedWkrs:   "error",
		},
		{
			name:           "unhealthy when both DB and workers are down",
			pingErr:        errors.New("timeout"),
			workers:        nil,
			workerErr:      errors.New("cache error"),
			expectedStatus: http.StatusServiceUnavailable,
			expectedDB:     "unhealthy",
			expectedWkrs:   "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := infoserver.NewServer(
				logger, "1.0", "1.0", "http://localhost", "test",
				nil, "0.1.0", "8.0.0",
				&fakePinger{err: tt.pingErr},
				&fakeWorkerFactory{workers: tt.workers, err: tt.workerErr},
			)

			req := httptest.NewRequest("GET", "/api/v1/health", nil)
			w := httptest.NewRecorder()
			server.Health(w, req)

			if w.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, w.Code)
			}

			var status infoserver.HealthStatus
			if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
				t.Fatalf("failed to decode response: %v", err)
			}

			if status.DB != tt.expectedDB {
				t.Errorf("expected DB=%q, got %q", tt.expectedDB, status.DB)
			}
			if status.Workers != tt.expectedWkrs {
				t.Errorf("expected Workers=%q, got %q", tt.expectedWkrs, status.Workers)
			}
		})
	}
}
