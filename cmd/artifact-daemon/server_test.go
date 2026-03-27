package main_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

func setupServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()

	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon")
	server := daemon.NewServer(logger, storagePath)
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, storagePath
}

func TestHealthz(t *testing.T) {
	ts, _ := setupServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestPutAndGetArtifact(t *testing.T) {
	ts, _ := setupServer(t)

	content := "hello artifact"

	// PUT
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/build-42-output.tar", strings.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("PUT: expected 201, got %d", resp.StatusCode)
	}

	// GET
	resp, err = http.Get(ts.URL + "/artifacts/build-42-output.tar")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET: expected 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != content {
		t.Errorf("GET body: expected %q, got %q", content, string(body))
	}
}

func TestHeadArtifactExists(t *testing.T) {
	ts, _ := setupServer(t)

	// PUT first
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/exists.tar", strings.NewReader("data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()

	// HEAD existing
	req, _ = http.NewRequest(http.MethodHead, ts.URL+"/artifacts/exists.tar", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("HEAD existing: expected 200, got %d", resp.StatusCode)
	}
}

func TestHeadArtifactMissing(t *testing.T) {
	ts, _ := setupServer(t)

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/artifacts/missing.tar", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("HEAD missing: expected 404, got %d", resp.StatusCode)
	}
}

func TestDeleteArtifact(t *testing.T) {
	ts, _ := setupServer(t)

	// PUT first
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/to-delete.tar", strings.NewReader("data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()

	// DELETE
	req, _ = http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/to-delete.tar", nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE: expected 204, got %d", resp.StatusCode)
	}

	// GET should now 404
	resp, err = http.Get(ts.URL + "/artifacts/to-delete.tar")
	if err != nil {
		t.Fatalf("GET after DELETE failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET after DELETE: expected 404, got %d", resp.StatusCode)
	}
}

func TestGetMissingArtifact(t *testing.T) {
	ts, _ := setupServer(t)

	resp, err := http.Get(ts.URL + "/artifacts/nonexistent.tar")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET missing: expected 404, got %d", resp.StatusCode)
	}
}

func TestNestedKeys(t *testing.T) {
	ts, storagePath := setupServer(t)

	content := "nested artifact data"

	// PUT with nested key
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/caches/job-42/build-abc.tar", strings.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT nested failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("PUT nested: expected 201, got %d", resp.StatusCode)
	}

	// Verify subdirectories were created on disk
	expectedPath := filepath.Join(storagePath, "artifacts", "caches", "job-42", "build-abc.tar")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("nested key did not create subdirectories: %s not found", expectedPath)
	}

	// GET should return the data
	resp, err = http.Get(ts.URL + "/artifacts/caches/job-42/build-abc.tar")
	if err != nil {
		t.Fatalf("GET nested failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != content {
		t.Errorf("GET nested: expected %q, got %q", content, string(body))
	}
}

func TestDeleteMissingArtifact(t *testing.T) {
	ts, _ := setupServer(t)

	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/nope.tar", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	resp.Body.Close()

	// DELETE on missing key should be idempotent (204)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("DELETE missing: expected 204, got %d", resp.StatusCode)
	}
}

func TestPutStoresAtCorrectPath(t *testing.T) {
	ts, storagePath := setupServer(t)

	content := "file content"
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/my-key.tar", strings.NewReader(content))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT failed: %v", err)
	}
	resp.Body.Close()

	// Verify file exists on disk at the right location
	expectedPath := filepath.Join(storagePath, "artifacts", "my-key.tar")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not found at %s: %v", expectedPath, err)
	}
	if string(data) != content {
		t.Errorf("file content: expected %q, got %q", content, string(data))
	}
}
