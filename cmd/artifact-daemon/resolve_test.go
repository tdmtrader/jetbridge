package main_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterEndpoint(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create a directory to register.
	artifactDir := filepath.Join(storagePath, "steps", "handle-1", "result")
	os.MkdirAll(artifactDir, 0755)
	os.WriteFile(filepath.Join(artifactDir, "data.txt"), []byte("hello"), 0644)

	// POST /register
	body := `{"key":"handle-1/result","local_path":"` + artifactDir + `"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /register: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

func TestRegisterEndpoint_MissingFields(t *testing.T) {
	ts, _ := setupServer(t)

	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(`{"key":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing local_path, got %d", resp.StatusCode)
	}

	resp, err = http.Post(ts.URL+"/register", "application/json", strings.NewReader(`{"local_path":"/x"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing key, got %d", resp.StatusCode)
	}
}

func TestRegisterEndpoint_PathNotFound(t *testing.T) {
	ts, _ := setupServer(t)

	body := `{"key":"nope","local_path":"/nonexistent/path"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for nonexistent path, got %d", resp.StatusCode)
	}
}

func TestResolveEndpoint_LocalRegistry(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create source artifact.
	srcDir := filepath.Join(storagePath, "steps", "handle-a", "out")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "output.txt"), []byte("artifact data"), 0644)

	// Register it.
	regBody := `{"key":"handle-a/out","local_path":"` + srcDir + `"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Create destination directory.
	destDir := filepath.Join(t.TempDir(), "input")

	// POST /resolve
	resolveBody := `{"key":"handle-a/out","dest":"` + destDir + `"}`
	resp, err = http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatalf("POST /resolve: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", result["status"])
	}
	if result["method"] != "registry" {
		t.Errorf("expected method=registry, got %v", result["method"])
	}

	// Verify file was copied.
	data, err := os.ReadFile(filepath.Join(destDir, "output.txt"))
	if err != nil {
		t.Fatalf("file not copied: %v", err)
	}
	if string(data) != "artifact data" {
		t.Errorf("expected 'artifact data', got %q", string(data))
	}
}

func TestResolveEndpoint_FilesystemFallback(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create artifact on disk but DON'T register it.
	srcDir := filepath.Join(storagePath, "steps", "handle-b", "dir")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("fallback data"), 0644)

	destDir := filepath.Join(t.TempDir(), "resolved")

	// POST /resolve — should find via filesystem scan fallback.
	resolveBody := `{"key":"handle-b/dir","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if result["method"] != "filesystem" {
		t.Errorf("expected method=filesystem, got %v", result["method"])
	}

	data, err := os.ReadFile(filepath.Join(destDir, "file.txt"))
	if err != nil {
		t.Fatalf("file not copied: %v", err)
	}
	if string(data) != "fallback data" {
		t.Errorf("expected 'fallback data', got %q", string(data))
	}
}

func TestResolveEndpoint_NotFound(t *testing.T) {
	ts, _ := setupServer(t)

	destDir := filepath.Join(t.TempDir(), "nope")
	body := `{"key":"nonexistent","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if result["status"] != "not_found" {
		t.Errorf("expected status=not_found, got %v", result["status"])
	}
}

func TestResolveEndpoint_MissingFields(t *testing.T) {
	ts, _ := setupServer(t)

	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(`{"key":"a"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for missing dest, got %d", resp.StatusCode)
	}
}

func TestResolveEndpoint_MultipleFiles(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create source with multiple files and subdirectories.
	srcDir := filepath.Join(storagePath, "steps", "handle-c", "result")
	os.MkdirAll(filepath.Join(srcDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(srcDir, "b.txt"), []byte("bbb"), 0644)
	os.WriteFile(filepath.Join(srcDir, "subdir", "c.txt"), []byte("ccc"), 0644)

	// Register and resolve.
	regBody := `{"key":"handle-c/result","local_path":"` + srcDir + `"}`
	resp, _ := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	resp.Body.Close()

	destDir := filepath.Join(t.TempDir(), "dest")
	resolveBody := `{"key":"handle-c/result","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Verify all files copied.
	for _, check := range []struct{ path, content string }{
		{"a.txt", "aaa"},
		{"b.txt", "bbb"},
		{"subdir/c.txt", "ccc"},
	} {
		data, err := os.ReadFile(filepath.Join(destDir, check.path))
		if err != nil {
			t.Errorf("missing %s: %v", check.path, err)
			continue
		}
		if string(data) != check.content {
			t.Errorf("%s: expected %q, got %q", check.path, check.content, string(data))
		}
	}
}

func TestResolveEndpoint_StartupScanThenResolve(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create artifact on disk BEFORE server starts (simulating pre-existing data).
	// The setupServer already ran, but we can simulate by creating the dir and
	// relying on the filesystem fallback (step 2 in resolve).
	srcDir := filepath.Join(storagePath, "steps", "old-handle", "output")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "legacy.txt"), []byte("old data"), 0644)

	destDir := filepath.Join(t.TempDir(), "legacy-dest")
	body := `{"key":"old-handle/output","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "legacy.txt"))
	if err != nil {
		t.Fatalf("file not resolved: %v", err)
	}
	if string(data) != "old data" {
		t.Errorf("expected 'old data', got %q", string(data))
	}
}
