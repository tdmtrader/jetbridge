package main_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func getMetrics(t *testing.T, baseURL string) string {
	t.Helper()
	resp, err := http.Get(baseURL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /metrics expected 200, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// TestMetricsEndpoint_Exposed verifies the daemon exposes a Prometheus scrape
// endpoint advertising the daemon metric families (peer_fetch_total is
// initialized to 0 so dashboards can rate() it from the start).
func TestMetricsEndpoint_Exposed(t *testing.T) {
	ts, _ := setupServer(t)

	body := getMetrics(t, ts.URL)
	if !strings.Contains(body, "artifact_daemon_peer_fetch_total") {
		t.Errorf("expected /metrics to expose artifact_daemon_peer_fetch_total, got:\n%s", body)
	}
}

// TestMetricsEndpoint_RecordsResolve verifies a successful local resolve
// increments resolve_requests_total{method,status} and observes the duration
// histogram.
func TestMetricsEndpoint_RecordsResolve(t *testing.T) {
	ts, storagePath := setupServer(t)

	srcDir := filepath.Join(storagePath, "steps", "handle-m", "out")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(srcDir, "data.txt"), []byte("x"), 0644)

	destDir := filepath.Join(t.TempDir(), "input")
	resp, err := http.Post(ts.URL+"/resolve", "application/json",
		strings.NewReader(`{"key":"handle-m/out","dest":"`+destDir+`"}`))
	if err != nil {
		t.Fatalf("POST /resolve: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve expected 200, got %d", resp.StatusCode)
	}

	body := getMetrics(t, ts.URL)
	if !strings.Contains(body, `artifact_daemon_resolve_requests_total{method="filesystem",status="ok"} 1`) {
		t.Errorf("expected resolve_requests_total{filesystem,ok}=1, got:\n%s", body)
	}
	if !strings.Contains(body, `artifact_daemon_resolve_duration_seconds_count{method="filesystem"} 1`) {
		t.Errorf("expected resolve_duration_seconds_count{filesystem}=1, got:\n%s", body)
	}
}

// TestMetricsEndpoint_RecordsNotFound verifies a missing artifact records a
// not_found resolve outcome.
func TestMetricsEndpoint_RecordsNotFound(t *testing.T) {
	ts, _ := setupServer(t)

	destDir := filepath.Join(t.TempDir(), "input")
	resp, err := http.Post(ts.URL+"/resolve", "application/json",
		strings.NewReader(`{"key":"does-not-exist","dest":"`+destDir+`"}`))
	if err != nil {
		t.Fatalf("POST /resolve: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resolve expected 404, got %d", resp.StatusCode)
	}

	body := getMetrics(t, ts.URL)
	if !strings.Contains(body, `artifact_daemon_resolve_requests_total{method="exhausted",status="not_found"} 1`) {
		t.Errorf("expected resolve_requests_total{exhausted,not_found}=1, got:\n%s", body)
	}
}
