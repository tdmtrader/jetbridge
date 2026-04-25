package main_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Cross-node peer probe tests: HEAD /artifacts/steps/{key} must find
// registry aliases so that peer daemons can discover cached resources.

func TestHeadArtifact_FindsRegistryAlias(t *testing.T) {
	ts, storagePath, server := setupServerWithRegistry(t)

	// Create step output and register as alias (mimics resource cache registration).
	dataDir := filepath.Join(storagePath, "steps", "container-xyz", "dir")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir, "file.txt"), []byte("cached"), 0644)
	server.Registry().RegisterAlias("rc-42", dataDir)

	// Peer probe sends HEAD /artifacts/steps/rc-42. The handler should find
	// the registry alias "rc-42" even though steps/rc-42 doesn't exist on disk.
	req, _ := http.NewRequest(http.MethodHead, ts.URL+"/artifacts/steps/rc-42", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for registry alias via /artifacts/ HEAD, got %d", resp.StatusCode)
	}
}

func TestGetArtifact_ServesRegistryAlias(t *testing.T) {
	ts, storagePath, server := setupServerWithRegistry(t)

	dataDir := filepath.Join(storagePath, "steps", "container-xyz", "dir")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dataDir, "data.txt"), []byte("hello from cache"), 0644)
	server.Registry().RegisterAlias("rc-99", dataDir)

	// Peer fetch sends GET /artifacts/steps/rc-99. The handler should serve
	// the data from the registered path.
	resp, err := http.Get(ts.URL + "/artifacts/steps/rc-99")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for registry alias via /artifacts/ GET, got %d", resp.StatusCode)
	}

	// Should be a tar stream of the directory.
	tr := tar.NewReader(resp.Body)
	found := false
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Name == "data.txt" {
			found = true
			data, _ := io.ReadAll(tr)
			if string(data) != "hello from cache" {
				t.Errorf("expected 'hello from cache', got %q", string(data))
			}
		}
	}
	if !found {
		t.Error("data.txt not found in tar stream from registry alias")
	}
}

// --- handleStreamIn tests ---

// makeTarBytes creates a tar archive containing a single file.
func makeTarBytes(t *testing.T, name, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: name, Size: int64(len(content)), Mode: 0644}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	return buf.Bytes()
}

func TestStreamIn_RawTar(t *testing.T) {
	ts, storagePath := setupServer(t)

	tarData := makeTarBytes(t, "task.yml", "platform: linux\n")

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/build-1", bytes.NewReader(tarData))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /stream-in/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify the file was extracted.
	data, err := os.ReadFile(filepath.Join(storagePath, "steps", "build-1", "task.yml"))
	if err != nil {
		t.Fatalf("extracted file not found: %v", err)
	}
	if string(data) != "platform: linux\n" {
		t.Errorf("expected 'platform: linux\\n', got %q", string(data))
	}
}

func TestStreamIn_GzippedTar(t *testing.T) {
	ts, storagePath := setupServer(t)

	tarData := makeTarBytes(t, "config.yml", "key: value\n")

	// Gzip the tar data.
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	gw.Write(tarData)
	gw.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/build-2", &gzBuf)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /stream-in/: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify the file was extracted.
	data, err := os.ReadFile(filepath.Join(storagePath, "steps", "build-2", "config.yml"))
	if err != nil {
		t.Fatalf("extracted file not found: %v", err)
	}
	if string(data) != "key: value\n" {
		t.Errorf("expected 'key: value\\n', got %q", string(data))
	}
}

func TestStreamIn_EmptyKey_Returns400(t *testing.T) {
	ts, _ := setupServer(t)

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/", strings.NewReader("data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty key, got %d", resp.StatusCode)
	}
}

// --- Permission hardening regression tests ---

// TestStreamIn_SetuidSetgidStripped verifies that setuid/setgid bits in tar
// headers are stripped during extraction (defense-in-depth).
func TestStreamIn_SetuidSetgidStripped(t *testing.T) {
	ts, storagePath := setupServer(t)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// File with setuid bit (mode 04755).
	content := []byte("#!/bin/sh\necho hi\n")
	tw.WriteHeader(&tar.Header{
		Name: "setuid-binary",
		Size: int64(len(content)),
		Mode: 04755,
	})
	tw.Write(content)

	// File with setgid bit (mode 02755).
	tw.WriteHeader(&tar.Header{
		Name: "setgid-binary",
		Size: int64(len(content)),
		Mode: 02755,
	})
	tw.Write(content)

	// File with both setuid+setgid (mode 06755).
	tw.WriteHeader(&tar.Header{
		Name: "suidsgid-binary",
		Size: int64(len(content)),
		Mode: 06755,
	})
	tw.Write(content)
	tw.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/suid-test", bytes.NewReader(buf.Bytes()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	extractDir := filepath.Join(storagePath, "steps", "suid-test")

	// Verify setuid stripped: 04755 → 0755.
	info, err := os.Stat(filepath.Join(extractDir, "setuid-binary"))
	if err != nil {
		t.Fatalf("setuid-binary not found: %v", err)
	}
	if info.Mode()&os.ModeSetuid != 0 {
		t.Errorf("setuid bit should be stripped, got mode %v", info.Mode())
	}

	// Verify setgid stripped: 02755 → 0755.
	info, err = os.Stat(filepath.Join(extractDir, "setgid-binary"))
	if err != nil {
		t.Fatalf("setgid-binary not found: %v", err)
	}
	if info.Mode()&os.ModeSetgid != 0 {
		t.Errorf("setgid bit should be stripped, got mode %v", info.Mode())
	}

	// Verify both stripped: 06755 → 0755.
	info, err = os.Stat(filepath.Join(extractDir, "suidsgid-binary"))
	if err != nil {
		t.Fatalf("suidsgid-binary not found: %v", err)
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		t.Errorf("setuid+setgid bits should be stripped, got mode %v", info.Mode())
	}
}

// TestStreamIn_RestrictiveModesNormalized verifies that restrictive permission
// modes in tar headers are normalized to a minimum floor (dirs ≥ 0755,
// files ≥ 0644) so the daemon can always read extracted artifacts.
func TestStreamIn_RestrictiveModesNormalized(t *testing.T) {
	ts, storagePath := setupServer(t)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Directory with mode 0700 (only owner can access).
	tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "restrictive-dir/",
		Mode:     0700,
	})

	// File with mode 0600 (only owner can read/write).
	content := []byte("secret data")
	tw.WriteHeader(&tar.Header{
		Name: "restrictive-dir/secret.txt",
		Size: int64(len(content)),
		Mode: 0600,
	})
	tw.Write(content)

	// File with mode 0000 (no access).
	tw.WriteHeader(&tar.Header{
		Name: "restrictive-dir/locked.txt",
		Size: int64(len(content)),
		Mode: 0000,
	})
	tw.Write(content)
	tw.Close()

	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/perm-test", bytes.NewReader(buf.Bytes()))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	extractDir := filepath.Join(storagePath, "steps", "perm-test")

	// Dir 0700 should be normalized to at least 0755.
	info, err := os.Stat(filepath.Join(extractDir, "restrictive-dir"))
	if err != nil {
		t.Fatalf("restrictive-dir not found: %v", err)
	}
	if info.Mode().Perm()&0755 != 0755 {
		t.Errorf("dir mode should have at least 0755, got %04o", info.Mode().Perm())
	}

	// File 0600 should be normalized to at least 0644.
	info, err = os.Stat(filepath.Join(extractDir, "restrictive-dir", "secret.txt"))
	if err != nil {
		t.Fatalf("secret.txt not found: %v", err)
	}
	if info.Mode().Perm()&0644 != 0644 {
		t.Errorf("file mode should have at least 0644, got %04o", info.Mode().Perm())
	}

	// File 0000 should be normalized to at least 0644.
	info, err = os.Stat(filepath.Join(extractDir, "restrictive-dir", "locked.txt"))
	if err != nil {
		t.Fatalf("locked.txt not found: %v", err)
	}
	if info.Mode().Perm()&0644 != 0644 {
		t.Errorf("file mode 0000 should be normalized to at least 0644, got %04o", info.Mode().Perm())
	}

	// Verify content is readable.
	data, err := os.ReadFile(filepath.Join(extractDir, "restrictive-dir", "secret.txt"))
	if err != nil {
		t.Fatalf("should be able to read normalized file: %v", err)
	}
	if string(data) != "secret data" {
		t.Errorf("expected 'secret data', got %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// POST /mirror — enqueues an outbound mirror job (P2a of
// artifact_daemon_resilience_20260425).
// ---------------------------------------------------------------------------

// recordingMirror collects keys passed to Trigger so tests can assert
// scheduling without running real network I/O.
type recordingMirror struct {
	mu   sync.Mutex
	keys []string
}

func (m *recordingMirror) Trigger(ctx context.Context, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.keys = append(m.keys, key)
}

func (m *recordingMirror) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.keys))
	copy(out, m.keys)
	return out
}

func TestPostMirror_EnqueuesJobAndReturns202(t *testing.T) {
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon")
	server := daemon.NewServer(logger, storagePath, "test-node")

	rec := &recordingMirror{}
	server.SetMirrorTrigger(rec.Trigger)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/mirror", "application/json",
		strings.NewReader(`{"key":"handle/output"}`))
	if err != nil {
		t.Fatalf("POST /mirror failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected 202 Accepted, got %d", resp.StatusCode)
	}

	calls := rec.calls()
	if len(calls) != 1 || calls[0] != "handle/output" {
		t.Errorf("expected Trigger to be called once with 'handle/output', got %v", calls)
	}
}

func TestPostMirror_EmptyKey_Returns400(t *testing.T) {
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon")
	server := daemon.NewServer(logger, storagePath, "test-node")

	rec := &recordingMirror{}
	server.SetMirrorTrigger(rec.Trigger)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/mirror", "application/json",
		strings.NewReader(`{"key":""}`))
	if err != nil {
		t.Fatalf("POST /mirror failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty key, got %d", resp.StatusCode)
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Errorf("expected no Trigger calls for empty key, got %v", calls)
	}
}

func TestPostMirror_InvalidJSON_Returns400(t *testing.T) {
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon")
	server := daemon.NewServer(logger, storagePath, "test-node")

	rec := &recordingMirror{}
	server.SetMirrorTrigger(rec.Trigger)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/mirror", "application/json",
		strings.NewReader(`not json`))
	if err != nil {
		t.Fatalf("POST /mirror failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// handleStreamIn → mirror trigger (P2a.7).
// ---------------------------------------------------------------------------

// makeTarPayload returns a small tar stream containing one file
// at "data.txt" with the given content. Used to drive PUT /stream-in/.
func makeTarPayload(t *testing.T, content string) *bytes.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name:     "data.txt",
		Mode:     0644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(buf.Bytes())
}

func TestStreamIn_SchedulesMirrorTrigger(t *testing.T) {
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("artifact-daemon")
	server := daemon.NewServer(logger, storagePath, "test-node")

	rec := &recordingMirror{}
	server.SetMirrorTrigger(rec.Trigger)

	ts := httptest.NewServer(server.Handler())
	defer ts.Close()

	body := makeTarPayload(t, "payload-bytes")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/handle/output", body)
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream-in failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("stream-in: expected 201, got %d", resp.StatusCode)
	}

	calls := rec.calls()
	if len(calls) != 1 || calls[0] != "handle/output" {
		t.Errorf("expected mirror trigger to fire once with key 'handle/output', got %v", calls)
	}
}

func TestStreamIn_MirrorTriggerOmitted_StillSucceeds(t *testing.T) {
	// No mirror trigger set: stream-in must still complete without panic
	// and without error. (Daemon running with mirror disabled.)
	ts, _ := setupServer(t)

	body := makeTarPayload(t, "payload")
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/stream-in/key/x", body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream-in failed: %v", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201 even without mirror, got %d", resp.StatusCode)
	}
}

