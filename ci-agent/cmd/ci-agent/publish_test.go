package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRunPublishUsesIndependentScopedPrincipalTokens(t *testing.T) {
	var mu sync.Mutex
	authByPath := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authByPath[r.URL.Path] = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	dir := t.TempDir()
	reviewPath := filepath.Join(dir, "review.json")
	if err := os.WriteFile(reviewPath, []byte(`{"schema_version":"1.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	costsPath := filepath.Join(dir, "costs.json")
	if err := os.WriteFile(costsPath, []byte(`[{"step":"analyze","cost_usd":0.25}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ATC_EXTERNAL_URL", server.URL)
	t.Setenv("BUILD_ID", "42")
	t.Setenv("AGENT_REVIEW_PRINCIPAL_TOKEN", "cap1.17.review")
	t.Setenv("AGENT_COST_PRINCIPAL_TOKEN", "cap1.18.cost")

	if code := runPublish([]string{"--review", reviewPath, "--costs", costsPath}); code != 0 {
		t.Fatalf("runPublish code = %d, want 0", code)
	}

	mu.Lock()
	defer mu.Unlock()
	if got := authByPath["/api/v1/agent/reviews"]; got != "Bearer cap1.17.review" {
		t.Errorf("review Authorization = %q", got)
	}
	if got := authByPath["/api/v1/agent/costs"]; got != "Bearer cap1.18.cost" {
		t.Errorf("cost Authorization = %q", got)
	}
}
