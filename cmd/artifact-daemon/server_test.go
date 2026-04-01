package main_test

import (
	"archive/tar"
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
	server := daemon.NewServer(logger, storagePath, "test-node")
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
	expectedPath := filepath.Join(storagePath, "caches", "job-42", "build-abc.tar")
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
	expectedPath := filepath.Join(storagePath, "my-key.tar")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not found at %s: %v", expectedPath, err)
	}
	if string(data) != content {
		t.Errorf("file content: expected %q, got %q", content, string(data))
	}
}

// Phase 5: Directory serving tests

func TestGetDirectoryTarsOnTheFly(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create a directory with files (simulating step output)
	stepDir := filepath.Join(storagePath, "steps", "build-42", "result")
	if err := os.MkdirAll(stepDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stepDir, "file1.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(stepDir, "file2.txt"), []byte("world"), 0644)

	resp, err := http.Get(ts.URL + "/artifacts/steps/build-42/result")
	if err != nil {
		t.Fatalf("GET directory: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Verify response is a valid tar stream containing the files
	tr := tar.NewReader(resp.Body)
	files := map[string]string{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		if hdr.Typeflag == tar.TypeReg {
			data, _ := io.ReadAll(tr)
			files[hdr.Name] = string(data)
		}
	}

	if files["file1.txt"] != "hello" {
		t.Errorf("expected file1.txt='hello', got %q", files["file1.txt"])
	}
	if files["file2.txt"] != "world" {
		t.Errorf("expected file2.txt='world', got %q", files["file2.txt"])
	}
}

func TestDeleteDirectoryRemovesTree(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create a directory tree
	stepDir := filepath.Join(storagePath, "steps", "build-99", "out")
	os.MkdirAll(stepDir, 0755)
	os.WriteFile(filepath.Join(stepDir, "data.bin"), []byte("x"), 0644)

	// DELETE the step directory (parent of output)
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/artifacts/steps/build-99", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}

	// Verify the entire tree is gone
	if _, err := os.Stat(filepath.Join(storagePath, "steps", "build-99")); !os.IsNotExist(err) {
		t.Error("expected directory tree to be removed")
	}
}

func TestHeadDirectoryReturns200(t *testing.T) {
	ts, storagePath := setupServer(t)

	stepDir := filepath.Join(storagePath, "steps", "build-50", "src")
	os.MkdirAll(stepDir, 0755)

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/artifacts/steps/build-50/src", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for existing directory, got %d", resp.StatusCode)
	}
}

// Resource cache endpoint tests

func setupServerWithRegistry(t *testing.T) (*httptest.Server, string, *daemon.Server) {
	t.Helper()
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon")
	server := daemon.NewServer(logger, storagePath, "test-node")
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts, storagePath, server
}

func TestHeadResourceCache_Found(t *testing.T) {
	ts, storagePath, server := setupServerWithRegistry(t)

	// Create a step output directory and register it as a resource cache alias.
	stepDir := filepath.Join(storagePath, "steps", "container-abc", "dir")
	if err := os.MkdirAll(stepDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stepDir, "file.txt"), []byte("cached data"), 0644)

	server.Registry().RegisterAlias("rc-42", stepDir)

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/rc-42", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Node-Name"); got != "test-node" {
		t.Errorf("expected X-Node-Name=test-node, got %q", got)
	}
}

func TestHeadResourceCache_NotFound(t *testing.T) {
	ts, _, _ := setupServerWithRegistry(t)

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/rc-999", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHeadResourceCache_StalePath(t *testing.T) {
	ts, _, server := setupServerWithRegistry(t)

	// Register an alias pointing to a path that doesn't exist on disk.
	server.Registry().RegisterAlias("rc-100", "/nonexistent/path")

	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/resource-caches/rc-100", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for stale path, got %d", resp.StatusCode)
	}

	// Verify the stale entry was cleaned up from the registry.
	if _, found := server.Registry().Lookup("rc-100"); found {
		t.Error("expected stale alias to be removed from registry")
	}
}

func TestGetResourceCache_StreamsDirectory(t *testing.T) {
	ts, storagePath, server := setupServerWithRegistry(t)

	// Create a cached directory.
	cacheDir := filepath.Join(storagePath, "steps", "get-handle", "dir")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(cacheDir, "version.txt"), []byte("abc123"), 0644)

	server.Registry().RegisterAlias("rc-7", cacheDir)

	resp, err := http.Get(ts.URL + "/resource-caches/rc-7")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Node-Name"); got != "test-node" {
		t.Errorf("expected X-Node-Name=test-node, got %q", got)
	}
	if resp.Header.Get("Content-Type") != "application/x-tar" {
		t.Errorf("expected application/x-tar, got %q", resp.Header.Get("Content-Type"))
	}

	// Verify the tar contains our file.
	tr := tar.NewReader(resp.Body)
	found := false
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "version.txt" {
			found = true
			data, _ := io.ReadAll(tr)
			if string(data) != "abc123" {
				t.Errorf("expected abc123, got %q", string(data))
			}
		}
	}
	if !found {
		t.Error("version.txt not found in tar stream")
	}
}
