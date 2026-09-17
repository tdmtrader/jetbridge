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
	Status  string            `json:"status"`
	Results []resolveResponse `json:"results"`
	Error   string            `json:"error,omitempty"`
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

	destA := destUnder(t, storagePath, "dest-a")
	destB := destUnder(t, storagePath, "dest-b")

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
//
// A missing artifact is 404, the same answer /resolve gives for the same
// outcome on one item. It used to be 500, and the difference is the whole
// diagnosis: an init container's BusyBox wget prints the status line and
// throws the body away, so 500 told an operator "the daemon is broken" for
// the one condition where the daemon is fine and the artifact is simply not
// there.
// ---------------------------------------------------------------------------

func TestResolveBatch_PartialFailure(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Only create one artifact.
	dir := filepath.Join(storagePath, "steps", "exists/output")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("ok"), 0644)

	destGood := destUnder(t, storagePath, "good")
	destBad := destUnder(t, storagePath, "bad")

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

	// A missing artifact is a miss, not a server error.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for a batch whose only failure is a missing artifact, got %d", resp.StatusCode)
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

	// Each result names its own artifact, and the batch names the failing one
	// in a single line. Without both, a caller reading this body knows only
	// that "something" in a list it did not keep went wrong.
	if result.Results[0].Key != "exists/output" || result.Results[1].Key != "missing/output" {
		t.Errorf("results do not name their artifacts: %q, %q",
			result.Results[0].Key, result.Results[1].Key)
	}
	if !strings.Contains(result.Error, "missing/output") {
		t.Errorf("batch summary does not name the missing artifact: %q", result.Error)
	}
	if strings.Contains(result.Error, "exists/output") {
		t.Errorf("batch summary names an artifact that resolved fine: %q", result.Error)
	}
}

// A real failure — one that is about this daemon rather than about whether an
// artifact exists — still has to be a 500, and has to outrank a miss in the
// same batch. Otherwise the new 404 would just move the ambiguity.
func TestResolveBatch_HardFailureOutranksAMiss(t *testing.T) {
	ts, storagePath := setupServer(t)

	dir := filepath.Join(storagePath, "steps", "present/output")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("ok"), 0644)

	// Contained, and validated as such, but its parent directory does not
	// exist — so the copy fails rather than the lookup.
	unwritable := filepath.Join(storagePath, "resolved", "no-such-parent", "dest")
	if err := os.MkdirAll(filepath.Join(storagePath, "resolved"), 0o755); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(batchRequest{
		Items: []batchItem{
			{Key: "absent/output", Dest: destUnder(t, storagePath, "miss")},
			{Key: "present/output", Dest: unwritable},
		},
	})

	resp, err := http.Post(ts.URL+"/resolve-batch", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("POST /resolve-batch: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500 when an item failed for a reason other than absence, got %d", resp.StatusCode)
	}

	var result batchResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if len(result.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	if result.Results[0].Status != "not_found" {
		t.Errorf("result[0]: expected not_found for the absent artifact, got %q", result.Results[0].Status)
	}
	if result.Results[1].Status != "error" {
		t.Errorf("result[1]: expected error for the failed copy, got %q (err %q)",
			result.Results[1].Status, result.Results[1].Error)
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
