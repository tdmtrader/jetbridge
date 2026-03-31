package main_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// batchRequest / batchResponse mirror the types in server.go.
type batchRequest struct {
	Items []batchItem `json:"items"`
}

type batchItem struct {
	Key  string `json:"key"`
	Dest string `json:"dest"`
}

type batchResponse struct {
	Status  string           `json:"status"`
	Results []resolveResponse `json:"results"`
}

// ---------------------------------------------------------------------------
// Happy path: batch resolve multiple artifacts
// ---------------------------------------------------------------------------

func TestResolveBatch_HappyPath(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create two artifacts on disk.
	for _, name := range []string{"handle-a/output", "handle-b/output"} {
		dir := filepath.Join(storagePath, "steps", name)
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "data.txt"), []byte("content-"+name), 0644)
	}

	destA := filepath.Join(t.TempDir(), "dest-a")
	destB := filepath.Join(t.TempDir(), "dest-b")

	body, _ := json.Marshal(batchRequest{
		Items: []batchItem{
			{Key: "handle-a/output", Dest: destA},
			{Key: "handle-b/output", Dest: destB},
		},
	})

	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result batchResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "ok" {
		t.Errorf("expected overall status=ok, got %q", result.Status)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	for i, r := range result.Results {
		if r.Status != "ok" {
			t.Errorf("result[%d]: expected status=ok, got %q (error: %s)", i, r.Status, r.Error)
		}
	}

	// Verify both artifacts were copied.
	dataA, err := os.ReadFile(filepath.Join(destA, "data.txt"))
	if err != nil {
		t.Fatalf("dest-a not populated: %v", err)
	}
	if string(dataA) != "content-handle-a/output" {
		t.Errorf("expected content for handle-a, got %q", string(dataA))
	}

	dataB, err := os.ReadFile(filepath.Join(destB, "data.txt"))
	if err != nil {
		t.Fatalf("dest-b not populated: %v", err)
	}
	if string(dataB) != "content-handle-b/output" {
		t.Errorf("expected content for handle-b, got %q", string(dataB))
	}
}

// ---------------------------------------------------------------------------
// Partial failure: one artifact exists, one does not
// ---------------------------------------------------------------------------

func TestResolveBatch_PartialFailure(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Only create one artifact.
	dir := filepath.Join(storagePath, "steps", "exists/output")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("ok"), 0644)

	destGood := filepath.Join(t.TempDir(), "good")
	destBad := filepath.Join(t.TempDir(), "bad")

	body, _ := json.Marshal(batchRequest{
		Items: []batchItem{
			{Key: "exists/output", Dest: destGood},
			{Key: "missing/output", Dest: destBad},
		},
	})

	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()

	// Partial failure → 500.
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 for partial failure, got %d", resp.StatusCode)
	}

	var result batchResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "error" {
		t.Errorf("expected overall status=error, got %q", result.Status)
	}
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}

	// First item should succeed.
	if result.Results[0].Status != "ok" {
		t.Errorf("result[0]: expected ok, got %q", result.Results[0].Status)
	}
	// Second item should fail.
	if result.Results[1].Status == "ok" {
		t.Errorf("result[1]: expected failure for missing artifact")
	}
}

// ---------------------------------------------------------------------------
// Empty batch
// ---------------------------------------------------------------------------

func TestResolveBatch_EmptyBatch(t *testing.T) {
	ts, _ := setupServer(t)

	body, _ := json.Marshal(batchRequest{Items: []batchItem{}})
	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for empty batch, got %d", resp.StatusCode)
	}

	var result batchResponse
	json.NewDecoder(resp.Body).Decode(&result)

	if result.Status != "ok" {
		t.Errorf("expected status=ok for empty batch, got %q", result.Status)
	}
	if len(result.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(result.Results))
	}
}

// ---------------------------------------------------------------------------
// Invalid JSON
// ---------------------------------------------------------------------------

func TestResolveBatch_InvalidJSON(t *testing.T) {
	ts, _ := setupServer(t)

	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader("not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}
