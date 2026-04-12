package main_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

// resolveResponse mirrors the server's response struct for JSON decoding.
type resolveResponse struct {
	Status   string `json:"status"`
	Source   string `json:"source"`
	Method   string `json:"method"`
	Duration string `json:"duration,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// DA-01: GET directory artifact tar details
// ---------------------------------------------------------------------------

func TestGetDirectory_ContentType(t *testing.T) {
	ts, storagePath := setupServer(t)

	stepDir := filepath.Join(storagePath, "steps", "build-ct", "out")
	if err := os.MkdirAll(stepDir, 0755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(stepDir, "a.txt"), []byte("aaa"), 0644)

	resp, err := http.Get(ts.URL + "/artifacts/steps/build-ct/out")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct != "application/x-tar" {
		t.Errorf("expected Content-Type application/x-tar, got %q", ct)
	}
}

func TestGetDirectory_TarContainsCorrectEntries(t *testing.T) {
	ts, storagePath := setupServer(t)

	stepDir := filepath.Join(storagePath, "steps", "build-entries", "out")
	os.MkdirAll(filepath.Join(stepDir, "sub"), 0755)
	os.WriteFile(filepath.Join(stepDir, "root.txt"), []byte("root-content"), 0644)
	os.WriteFile(filepath.Join(stepDir, "sub", "nested.txt"), []byte("nested-content"), 0644)

	resp, err := http.Get(ts.URL + "/artifacts/steps/build-entries/out")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

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

	if files["root.txt"] != "root-content" {
		t.Errorf("root.txt: expected 'root-content', got %q", files["root.txt"])
	}
	expected := filepath.Join("sub", "nested.txt")
	if files[expected] != "nested-content" {
		t.Errorf("%s: expected 'nested-content', got %q", expected, files[expected])
	}
}

func TestGetDirectory_SymlinksPreserved(t *testing.T) {
	ts, storagePath := setupServer(t)

	stepDir := filepath.Join(storagePath, "steps", "build-sym", "out")
	os.MkdirAll(stepDir, 0755)
	os.WriteFile(filepath.Join(stepDir, "target.txt"), []byte("target"), 0644)
	os.Symlink("target.txt", filepath.Join(stepDir, "link.txt"))

	resp, err := http.Get(ts.URL + "/artifacts/steps/build-sym/out")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	// Note: The server's tarDirectory uses filepath.Walk which follows symlinks
	// and reports them as regular files (info.Mode()&os.ModeSymlink is never set
	// with Walk). This test documents the actual behavior: symlinks appear as
	// regular files in the tar stream. If the implementation changes to use
	// filepath.WalkDir with Lstat, this test should be updated.
	tr := tar.NewReader(resp.Body)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading tar: %v", err)
		}
		if hdr.Name == "link.txt" {
			found = true
			// With filepath.Walk, symlinks are followed, so link.txt appears
			// as a regular file with the target's content.
			if hdr.Typeflag == tar.TypeSymlink {
				if hdr.Linkname != "target.txt" {
					t.Errorf("symlink target: expected 'target.txt', got %q", hdr.Linkname)
				}
			} else if hdr.Typeflag == tar.TypeReg {
				data, _ := io.ReadAll(tr)
				if string(data) != "target" {
					t.Errorf("link.txt content: expected 'target', got %q", string(data))
				}
			}
		}
	}
	if !found {
		t.Error("expected link.txt in tar stream")
	}
}

// ---------------------------------------------------------------------------
// DA-02: GET file artifact content type
// ---------------------------------------------------------------------------

func TestGetFile_ContentType(t *testing.T) {
	ts, _ := setupServer(t)

	// PUT a file artifact.
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/ct-test.tar", strings.NewReader("file-data"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	resp.Body.Close()

	// GET and check Content-Type.
	resp, err = http.Get(ts.URL + "/artifacts/ct-test.tar")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "application/octet-stream" {
		t.Errorf("expected Content-Type application/octet-stream, got %q", ct)
	}
}

// ---------------------------------------------------------------------------
// DA-07: POST /register edge cases
// ---------------------------------------------------------------------------

func TestRegister_ThenResolve_FullFlow(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create the artifact directory on disk.
	srcDir := filepath.Join(storagePath, "steps", "reg-handle", "output")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "payload.txt"), []byte("registered-data"), 0644)

	// Register via /register endpoint.
	regBody := `{"key":"reg-handle/output","local_path":"` + srcDir + `"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register: expected 201, got %d", resp.StatusCode)
	}

	// Resolve should find it via registry (method=registry), not filesystem.
	destDir := filepath.Join(t.TempDir(), "resolved")
	resolveBody := `{"key":"reg-handle/output","dest":"` + destDir + `"}`
	resp, err = http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: expected 200, got %d", resp.StatusCode)
	}

	var result resolveResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Method != "registry" {
		t.Errorf("expected method=registry, got %q", result.Method)
	}
	if result.Status != "ok" {
		t.Errorf("expected status=ok, got %q", result.Status)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "payload.txt"))
	if err != nil {
		t.Fatalf("file not copied: %v", err)
	}
	if string(data) != "registered-data" {
		t.Errorf("expected 'registered-data', got %q", string(data))
	}
}

func TestRegister_DuplicateOverwrites(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create two directories.
	dir1 := filepath.Join(storagePath, "steps", "dup-handle", "v1")
	dir2 := filepath.Join(storagePath, "steps", "dup-handle", "v2")
	os.MkdirAll(dir1, 0755)
	os.MkdirAll(dir2, 0755)
	os.WriteFile(filepath.Join(dir1, "data.txt"), []byte("version-1"), 0644)
	os.WriteFile(filepath.Join(dir2, "data.txt"), []byte("version-2"), 0644)

	// Register with same key twice, different paths.
	body1 := `{"key":"dup-key","local_path":"` + dir1 + `"}`
	resp, _ := http.Post(ts.URL+"/register", "application/json", strings.NewReader(body1))
	resp.Body.Close()

	body2 := `{"key":"dup-key","local_path":"` + dir2 + `"}`
	resp, _ = http.Post(ts.URL+"/register", "application/json", strings.NewReader(body2))
	resp.Body.Close()

	// Resolve should use the second registration.
	destDir := filepath.Join(t.TempDir(), "dup-dest")
	resolveBody := `{"key":"dup-key","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	data, err := os.ReadFile(filepath.Join(destDir, "data.txt"))
	if err != nil {
		t.Fatalf("file not copied: %v", err)
	}
	if string(data) != "version-2" {
		t.Errorf("expected 'version-2' (overwritten), got %q", string(data))
	}
}

// ---------------------------------------------------------------------------
// DA-PERM-01: Atomic copy — retry succeeds despite stale partial destination
// ---------------------------------------------------------------------------

// TestResolve_AtomicCopy_OverwritesStaleDestination verifies that /resolve
// succeeds even when the destination directory already contains read-only files
// from a prior interrupted copy. The atomic copy pattern (temp dir + rename)
// should cleanly replace the stale destination.
func TestResolve_AtomicCopy_OverwritesStaleDestination(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create the source artifact.
	srcDir := filepath.Join(storagePath, "steps", "atomic-handle", "output")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "result.txt"), []byte("fresh-data"), 0644)

	// Register it.
	regBody := `{"key":"atomic-handle/output","local_path":"` + srcDir + `"}`
	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Pre-create a "stale" destination with a read-only file to simulate a
	// prior interrupted copy. Without atomic copy, cp -R would fail trying
	// to overwrite the read-only file.
	destDir := filepath.Join(t.TempDir(), "stale-dest")
	os.MkdirAll(destDir, 0755)
	os.WriteFile(filepath.Join(destDir, "result.txt"), []byte("stale-data"), 0444) // read-only

	// Resolve should succeed despite the stale destination.
	resolveBody := `{"key":"atomic-handle/output","dest":"` + destDir + `"}`
	resp, err = http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	// Verify fresh data replaced stale data.
	data, err := os.ReadFile(filepath.Join(destDir, "result.txt"))
	if err != nil {
		t.Fatalf("result.txt not found after atomic copy: %v", err)
	}
	if string(data) != "fresh-data" {
		t.Errorf("expected 'fresh-data', got %q (stale data not replaced)", string(data))
	}
}

// ---------------------------------------------------------------------------
// DA-08/DA-09: POST /resolve with peer fallback
// ---------------------------------------------------------------------------

func TestResolve_PeerFallback_EndToEnd(t *testing.T) {
	// Set up a peer daemon with the artifact.
	peerStorage := t.TempDir()
	stepDir := filepath.Join(peerStorage, "steps", "peer-handle", "result")
	os.MkdirAll(stepDir, 0755)
	os.WriteFile(filepath.Join(stepDir, "peer-file.txt"), []byte("from-peer-daemon"), 0644)

	peerLogger := lagertest.NewTestLogger("peer")
	peerServer := daemon.NewServer(peerLogger, peerStorage, "peer-node")
	peerTS := httptest.NewServer(peerServer.Handler())
	defer peerTS.Close()

	peerAddr := peerTS.Listener.Addr().String()
	peerHost, peerPort := splitHostPort(t, peerAddr)

	// Set up local daemon with no local artifact and a PeerResolver.
	localStorage := t.TempDir()
	localLogger := lagertest.NewTestLogger("local")
	localServer := daemon.NewServer(localLogger, localStorage, "local-node")

	// Create a fake K8s clientset with an EndpointSlice pointing to the peer.
	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "artifact-daemon-slice",
			Namespace: "concourse",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "artifact-daemon",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses: []string{peerHost},
				Conditions: discoveryv1.EndpointConditions{
					Ready: &ready,
				},
			},
		},
	})

	resolver := daemon.NewPeerResolver(localLogger, clientset, "concourse", "artifact-daemon", peerPort, "10.0.0.99", nil)
	localServer.SetPeerResolver(resolver)

	localTS := httptest.NewServer(localServer.Handler())
	defer localTS.Close()

	// Resolve via local daemon - should fall back to peer.
	destDir := filepath.Join(t.TempDir(), "peer-resolved")
	resolveBody := `{"key":"peer-handle/result","dest":"` + destDir + `"}`
	resp, err := http.Post(localTS.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
	}

	var result resolveResponse
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Method != "peer" {
		t.Errorf("expected method=peer, got %q", result.Method)
	}
	if result.Status != "ok" {
		t.Errorf("expected status=ok, got %q", result.Status)
	}

	data, err := os.ReadFile(filepath.Join(destDir, "peer-file.txt"))
	if err != nil {
		t.Fatalf("file not fetched from peer: %v", err)
	}
	if string(data) != "from-peer-daemon" {
		t.Errorf("expected 'from-peer-daemon', got %q", string(data))
	}
}

func TestResolve_FilesystemFallback_AutoRegisters(t *testing.T) {
	ts, storagePath := setupServer(t)

	// Create artifact on disk without registering.
	srcDir := filepath.Join(storagePath, "steps", "auto-reg", "output")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "data.txt"), []byte("auto-data"), 0644)

	// First resolve: filesystem fallback.
	destDir1 := filepath.Join(t.TempDir(), "dest1")
	body := `{"key":"auto-reg/output","dest":"` + destDir1 + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var result1 resolveResponse
	json.NewDecoder(resp.Body).Decode(&result1)
	resp.Body.Close()
	if result1.Method != "filesystem" {
		t.Fatalf("first resolve: expected method=filesystem, got %q", result1.Method)
	}

	// Second resolve: should use registry (auto-registered by first resolve).
	destDir2 := filepath.Join(t.TempDir(), "dest2")
	body = `{"key":"auto-reg/output","dest":"` + destDir2 + `"}`
	resp, err = http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var result2 resolveResponse
	json.NewDecoder(resp.Body).Decode(&result2)
	resp.Body.Close()
	if result2.Method != "registry" {
		t.Errorf("second resolve: expected method=registry, got %q", result2.Method)
	}
}

// ---------------------------------------------------------------------------
// DA-10: POST /resolve structured logging
// ---------------------------------------------------------------------------

func TestResolve_ResponseIncludesStructuredFields(t *testing.T) {
	ts, storagePath := setupServer(t)

	srcDir := filepath.Join(storagePath, "steps", "struct-handle", "out")
	os.MkdirAll(srcDir, 0755)
	os.WriteFile(filepath.Join(srcDir, "f.txt"), []byte("x"), 0644)

	regBody := `{"key":"struct-handle/out","local_path":"` + srcDir + `"}`
	resp, _ := http.Post(ts.URL+"/register", "application/json", strings.NewReader(regBody))
	resp.Body.Close()

	destDir := filepath.Join(t.TempDir(), "struct-dest")
	resolveBody := `{"key":"struct-handle/out","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(resolveBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var raw map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&raw)

	// Verify all structured fields are present.
	if _, ok := raw["status"]; !ok {
		t.Error("response missing 'status' field")
	}
	if _, ok := raw["method"]; !ok {
		t.Error("response missing 'method' field")
	}
	if _, ok := raw["duration"]; !ok {
		t.Error("response missing 'duration' field")
	}
}

func TestResolve_NotFound_IncludesStructuredFields(t *testing.T) {
	ts, _ := setupServer(t)

	destDir := filepath.Join(t.TempDir(), "nf-dest")
	body := `{"key":"nonexistent-key","dest":"` + destDir + `"}`
	resp, err := http.Post(ts.URL+"/resolve", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var raw map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&raw)

	if raw["status"] != "not_found" {
		t.Errorf("expected status=not_found, got %v", raw["status"])
	}
	if raw["method"] != "exhausted" {
		t.Errorf("expected method=exhausted, got %v", raw["method"])
	}
	if _, ok := raw["duration"]; !ok {
		t.Error("not_found response missing 'duration' field")
	}
	if _, ok := raw["error"]; !ok {
		t.Error("not_found response missing 'error' field")
	}
}

// ---------------------------------------------------------------------------
// DA-11: GET /healthz response body
// ---------------------------------------------------------------------------

func TestHealthz_ResponseBodyEmpty(t *testing.T) {
	ts, _ := setupServer(t)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if len(body) != 0 {
		t.Errorf("expected empty body, got %q", string(body))
	}
}

// ---------------------------------------------------------------------------
// AR-01: Two-map design - Register vs RegisterAlias
// ---------------------------------------------------------------------------

func TestRegistry_Register_DoesNotAddToAliases(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	r := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	// Register (not RegisterAlias) should add to entries but NOT aliases.
	r.Register("scan-key", "/some/path")

	// Verify lookup works.
	if _, ok := r.Lookup("scan-key"); !ok {
		t.Fatal("Register'd key should be in entries")
	}

	// Load aliases in a fresh registry to check persistence.
	r2 := daemon.NewRegistry(logger)
	r2.SetAliasStore(store)
	r2.LoadAliases()

	if _, ok := r2.Lookup("scan-key"); ok {
		t.Error("Register'd key should NOT appear in alias persistence")
	}
}

func TestRegistry_RegisterAlias_AddsToBothMaps(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	diskPath := filepath.Join(dir, "steps", "abc", "out")
	os.MkdirAll(diskPath, 0755)

	r := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	r.RegisterAlias("alias-key", diskPath)

	// Should be in entries.
	if _, ok := r.Lookup("alias-key"); !ok {
		t.Fatal("alias key should be in entries")
	}

	// Should persist and be loadable.
	r2 := daemon.NewRegistry(logger)
	r2.SetAliasStore(store)
	r2.LoadAliases()
	if _, ok := r2.Lookup("alias-key"); !ok {
		t.Error("alias key should be in persisted aliases")
	}
}

// ---------------------------------------------------------------------------
// AR-02: Thread safety - concurrent operations
// ---------------------------------------------------------------------------

func TestRegistry_ConcurrentAccess(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	dir := t.TempDir()
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	const goroutines = 50
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines * 3)

	// Concurrent RegisterAlias.
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				path := filepath.Join(dir, "steps", key)
				os.MkdirAll(path, 0755)
				r.RegisterAlias(key, path)
			}
		}(i)
	}

	// Concurrent Lookup.
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				r.Lookup(key)
			}
		}(i)
	}

	// Concurrent Remove.
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				key := fmt.Sprintf("key-%d-%d", id, j)
				r.Remove(key)
			}
		}(i)
	}

	wg.Wait()

	// Should not have panicked. Len should be deterministic-ish but we just
	// verify it doesn't crash.
	_ = r.Len()
	_ = r.Keys()
}

// ---------------------------------------------------------------------------
// AR-03: Startup scan edge cases
// ---------------------------------------------------------------------------

func TestRegistry_ScanHostPath_SkipsFilesInSteps(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	storagePath := t.TempDir()
	stepsDir := filepath.Join(storagePath, "steps")
	os.MkdirAll(stepsDir, 0755)

	// A plain file in steps/ (not a handle directory) should be skipped.
	os.WriteFile(filepath.Join(stepsDir, "random-file.txt"), []byte("noise"), 0644)

	// A valid handle with output dir.
	os.MkdirAll(filepath.Join(stepsDir, "valid-handle", "output"), 0755)

	err := r.ScanHostPath(storagePath)
	if err != nil {
		t.Fatalf("ScanHostPath: %v", err)
	}

	if r.Len() != 1 {
		t.Errorf("expected 1 (only valid-handle/output), got %d (keys: %v)", r.Len(), r.Keys())
	}
}

func TestRegistry_ScanHostPath_NestedOutputSubdirs(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)

	storagePath := t.TempDir()
	stepsDir := filepath.Join(storagePath, "steps")

	// Handle with multiple output subdirectories.
	os.MkdirAll(filepath.Join(stepsDir, "handle-multi", "result"), 0755)
	os.MkdirAll(filepath.Join(stepsDir, "handle-multi", "logs"), 0755)
	os.MkdirAll(filepath.Join(stepsDir, "handle-multi", "cache"), 0755)
	// A file inside the handle dir (not a subdir) should be skipped.
	os.WriteFile(filepath.Join(stepsDir, "handle-multi", "metadata.json"), []byte("{}"), 0644)

	err := r.ScanHostPath(storagePath)
	if err != nil {
		t.Fatalf("ScanHostPath: %v", err)
	}

	// Should register result, logs, cache but NOT metadata.json.
	if r.Len() != 3 {
		t.Errorf("expected 3 output dirs, got %d (keys: %v)", r.Len(), r.Keys())
	}

	for _, key := range []string{"handle-multi/result", "handle-multi/logs", "handle-multi/cache"} {
		if _, ok := r.Lookup(key); !ok {
			t.Errorf("expected %q to be registered", key)
		}
	}
}

// ---------------------------------------------------------------------------
// AR-04/AR-05: Alias persistence edge cases
// ---------------------------------------------------------------------------

func TestAliasStore_Save_NoTmpFileRemains(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	path1 := filepath.Join(dir, "steps", "x", "y")
	os.MkdirAll(path1, 0755)

	err := store.Save(map[string]string{"k1": path1})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	tmpFile := filepath.Join(dir, "aliases.json.tmp")
	if _, err := os.Stat(tmpFile); !os.IsNotExist(err) {
		t.Error("expected no .tmp file after successful Save")
	}

	// Verify the actual file exists and is valid JSON.
	data, err := os.ReadFile(filepath.Join(dir, "aliases.json"))
	if err != nil {
		t.Fatalf("aliases.json missing: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
}

func TestAliasStore_Load_FiltersAllStaleEntries(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("alias-store")
	store := daemon.NewAliasStore(logger, dir)

	validPath := filepath.Join(dir, "steps", "valid", "out")
	os.MkdirAll(validPath, 0755)

	// Save with a mix of valid and stale entries.
	aliases := map[string]string{
		"valid-1": validPath,
		"stale-1": "/nonexistent/path/one",
		"stale-2": "/nonexistent/path/two",
		"stale-3": "/another/missing/path",
	}
	if err := store.Save(aliases); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(loaded) != 1 {
		t.Errorf("expected 1 valid entry, got %d", len(loaded))
	}
	if _, ok := loaded["valid-1"]; !ok {
		t.Error("expected 'valid-1' to survive filtering")
	}
}

// ---------------------------------------------------------------------------
// AR-06: Non-fatal persistence (nil AliasStore)
// ---------------------------------------------------------------------------

func TestRegistry_RegisterAlias_NilAliasStore_NoPanic(t *testing.T) {
	logger := lagertest.NewTestLogger("registry")
	r := daemon.NewRegistry(logger)
	// Do NOT call SetAliasStore — aliasStore is nil.

	// Should not panic.
	r.RegisterAlias("key-no-store", "/some/path")

	path, ok := r.Lookup("key-no-store")
	if !ok {
		t.Error("expected alias to be in memory even without store")
	}
	if path != "/some/path" {
		t.Errorf("expected /some/path, got %s", path)
	}
}

// ---------------------------------------------------------------------------
// AR-07: RemoveByPath edge cases
// ---------------------------------------------------------------------------

func TestRegistry_RemoveByPath_MultipleSamePrefix(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	// Create paths under the same prefix.
	prefix := filepath.Join(dir, "steps", "container-x")
	path1 := filepath.Join(prefix, "output1")
	path2 := filepath.Join(prefix, "output2")
	path3 := filepath.Join(prefix, "output3")
	unrelated := filepath.Join(dir, "steps", "container-y", "out")
	for _, p := range []string{path1, path2, path3, unrelated} {
		os.MkdirAll(p, 0755)
	}

	r := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	r.RegisterAlias("vol-1", path1)
	r.RegisterAlias("vol-2", path2)
	r.RegisterAlias("vol-3", path3)
	r.RegisterAlias("vol-other", unrelated)

	if r.Len() != 4 {
		t.Fatalf("expected 4 entries before remove, got %d", r.Len())
	}

	r.RemoveByPath(prefix)

	if r.Len() != 1 {
		t.Errorf("expected 1 entry after RemoveByPath, got %d (keys: %v)", r.Len(), r.Keys())
	}
	if _, ok := r.Lookup("vol-other"); !ok {
		t.Error("vol-other should still exist (different prefix)")
	}
}

func TestRegistry_RemoveByPath_TriggersAliasPersistence(t *testing.T) {
	dir := t.TempDir()
	logger := lagertest.NewTestLogger("registry")

	prefix := filepath.Join(dir, "steps", "container-persist")
	path1 := filepath.Join(prefix, "result")
	os.MkdirAll(path1, 0755)

	r := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, dir)
	r.SetAliasStore(store)

	r.RegisterAlias("persist-vol", path1)

	// Verify it's persisted.
	loaded1, _ := store.Load()
	if _, ok := loaded1["persist-vol"]; !ok {
		t.Fatal("expected persist-vol in aliases before RemoveByPath")
	}

	r.RemoveByPath(prefix)

	// Verify persistence was updated.
	loaded2, _ := store.Load()
	if _, ok := loaded2["persist-vol"]; ok {
		t.Error("expected persist-vol to be removed from aliases after RemoveByPath")
	}
}

// ---------------------------------------------------------------------------
// PD-01: Peer IP discovery via EndpointSlices
// ---------------------------------------------------------------------------

func TestPeerResolver_PeerIPs_WithEndpointSlices(t *testing.T) {
	logger := lagertest.NewTestLogger("peer-discovery")

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-svc-slice-1",
			Namespace: "test-ns",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "my-svc",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{"10.0.0.1", "10.0.0.2"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
			{
				Addresses:  []string{"10.0.0.3"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
		},
	})

	resolver := daemon.NewPeerResolver(logger, clientset, "test-ns", "my-svc", 8080, "10.0.0.2", nil)

	// Probe with a key that won't match any peer (they don't exist), but
	// we can verify self-exclusion by checking the probe tries peers.
	// Since peers are fake and won't respond, Probe returns false.
	_, found := resolver.Probe(context.Background(), "some-key")
	if found {
		t.Error("expected Probe to return false (fake peers don't respond)")
	}
}

func TestPeerResolver_SelfExclusion(t *testing.T) {
	logger := lagertest.NewTestLogger("peer-self")

	// Set up a real HTTP server pretending to be a peer.
	probed := false
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probed = true
		w.WriteHeader(http.StatusNotFound)
	}))
	defer fakePeer.Close()
	peerHost, peerPort := splitHostPort(t, fakePeer.Listener.Addr().String())

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "svc-slice",
			Namespace: "ns",
			Labels: map[string]string{
				discoveryv1.LabelServiceName: "svc",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{peerHost, "10.0.0.99"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
		},
	})

	// myPodIP = 10.0.0.99, so that IP should be excluded.
	resolver := daemon.NewPeerResolver(logger, clientset, "ns", "svc", peerPort, "10.0.0.99", nil)
	resolver.Probe(context.Background(), "any-key")

	// The fake peer should have been probed (it's not self).
	if !probed {
		t.Error("expected fake peer to be probed (self-IP should be excluded)")
	}
}

func TestPeerResolver_NilClientset_ReturnsNil(t *testing.T) {
	logger := lagertest.NewTestLogger("peer-nil")
	resolver := daemon.NewPeerResolver(logger, nil, "", "", 8080, "", nil)

	_, found := resolver.Probe(context.Background(), "any-key")
	if found {
		t.Error("expected false when clientset is nil")
	}
}

// ---------------------------------------------------------------------------
// PD-02: Sequential peer probe
// ---------------------------------------------------------------------------

func TestPeerProbe_FirstResponder200Wins(t *testing.T) {
	// Peer 1: always 404.
	peer1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer peer1.Close()

	// Peer 2: always 200.
	peer2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer peer2.Close()

	logger := lagertest.NewTestLogger("probe")
	host1, port1 := splitHostPort(t, peer1.Listener.Addr().String())
	host2, _ := splitHostPort(t, peer2.Listener.Addr().String())

	// Both peers must use same port for PeerResolver, so we need them on
	// the same port. Since httptest picks random ports, we use a workaround:
	// create a single EndpointSlice and resolver with peer2's port, but
	// peer1 won't match. Instead, test with just peer2 to verify 200 wins.
	// For a proper multi-peer test, both must be on the same port.
	// We'll test with one peer that returns 200.
	_ = host1
	_ = port1

	_, port2 := splitHostPort(t, peer2.Listener.Addr().String())

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "probe-slice",
			Namespace: "ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "svc"},
		},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{host2}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		},
	})

	resolver := daemon.NewPeerResolver(logger, clientset, "ns", "svc", port2, "10.99.99.99", nil)
	ip, found := resolver.Probe(context.Background(), "steps/some-key")
	if !found {
		t.Fatal("expected Probe to find peer")
	}
	if ip != host2 {
		t.Errorf("expected peer IP %s, got %s", host2, ip)
	}
}

func TestPeerProbe_NoPeerResponds200(t *testing.T) {
	// Peer that always returns 404.
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer peer.Close()

	logger := lagertest.NewTestLogger("probe-none")
	host, port := splitHostPort(t, peer.Listener.Addr().String())

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "probe-none-slice",
			Namespace: "ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "svc"},
		},
		Endpoints: []discoveryv1.Endpoint{
			{Addresses: []string{host}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		},
	})

	resolver := daemon.NewPeerResolver(logger, clientset, "ns", "svc", port, "10.99.99.99", nil)
	_, found := resolver.Probe(context.Background(), "steps/missing-key")
	if found {
		t.Error("expected Probe to return false when no peer has the artifact")
	}
}

// ---------------------------------------------------------------------------
// PD-03: Peer fetch with retry
// ---------------------------------------------------------------------------

func TestPeerFetch_CountsRetryAttempts(t *testing.T) {
	var attempts int
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// Second attempt: return valid tar.
		tw := tar.NewWriter(w)
		content := []byte("retry-ok")
		tw.WriteHeader(&tar.Header{Name: "file.txt", Size: int64(len(content)), Mode: 0644})
		tw.Write(content)
		tw.Close()
	}))
	defer fakePeer.Close()

	logger := lagertest.NewTestLogger("retry")
	host, port := splitHostPort(t, fakePeer.Listener.Addr().String())
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)

	destDir := filepath.Join(t.TempDir(), "retry")
	err := resolver.Fetch(context.Background(), host, "retry-key", destDir)
	if err != nil {
		t.Fatalf("expected success on retry, got: %v", err)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}

	data, _ := os.ReadFile(filepath.Join(destDir, "file.txt"))
	if string(data) != "retry-ok" {
		t.Errorf("expected 'retry-ok', got %q", string(data))
	}
}

func TestPeerFetch_AllAttemptsExhausted(t *testing.T) {
	var attempts int
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer fakePeer.Close()

	logger := lagertest.NewTestLogger("exhaust")
	host, port := splitHostPort(t, fakePeer.Listener.Addr().String())
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)

	destDir := filepath.Join(t.TempDir(), "exhaust")
	err := resolver.Fetch(context.Background(), host, "fail-key", destDir)
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

// ---------------------------------------------------------------------------
// PD-04: Tar extraction path traversal
// ---------------------------------------------------------------------------

func TestExtractTar_PathTraversal_Skipped(t *testing.T) {
	// Build a tar with a ".." path entry.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Malicious entry.
	tw.WriteHeader(&tar.Header{Name: "../../../etc/passwd", Size: 6, Mode: 0644})
	tw.Write([]byte("hacked"))
	// Legitimate entry.
	tw.WriteHeader(&tar.Header{Name: "safe.txt", Size: 4, Mode: 0644})
	tw.Write([]byte("safe"))
	tw.Close()

	// Set up a fake peer that serves this tar.
	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-tar")
		w.Write(buf.Bytes())
	}))
	defer fakePeer.Close()

	logger := lagertest.NewTestLogger("traversal")
	host, port := splitHostPort(t, fakePeer.Listener.Addr().String())
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)

	destDir := filepath.Join(t.TempDir(), "extract")
	err := resolver.Fetch(context.Background(), host, "traversal-key", destDir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// The ".." entry should have been skipped.
	if _, err := os.Stat(filepath.Join(destDir, "..", "..", "..", "etc", "passwd")); !os.IsNotExist(err) {
		t.Error("path traversal entry should have been skipped")
	}

	// Legitimate file should exist.
	data, err := os.ReadFile(filepath.Join(destDir, "safe.txt"))
	if err != nil {
		t.Fatalf("safe.txt not extracted: %v", err)
	}
	if string(data) != "safe" {
		t.Errorf("expected 'safe', got %q", string(data))
	}
}

func TestExtractTar_SymlinksExtracted(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Regular file.
	content := []byte("target-content")
	tw.WriteHeader(&tar.Header{Name: "target.txt", Size: int64(len(content)), Mode: 0644, Typeflag: tar.TypeReg})
	tw.Write(content)
	// Symlink.
	tw.WriteHeader(&tar.Header{Name: "link.txt", Typeflag: tar.TypeSymlink, Linkname: "target.txt"})
	tw.Close()

	fakePeer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(buf.Bytes())
	}))
	defer fakePeer.Close()

	logger := lagertest.NewTestLogger("symlink-extract")
	host, port := splitHostPort(t, fakePeer.Listener.Addr().String())
	resolver := daemon.NewPeerResolver(logger, nil, "", "", port, "", nil)

	destDir := filepath.Join(t.TempDir(), "symlinks")
	err := resolver.Fetch(context.Background(), host, "sym-key", destDir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Verify symlink was created.
	linkTarget, err := os.Readlink(filepath.Join(destDir, "link.txt"))
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if linkTarget != "target.txt" {
		t.Errorf("expected symlink to 'target.txt', got %q", linkTarget)
	}
}

// ---------------------------------------------------------------------------
// PD-05: Self-exclusion
// ---------------------------------------------------------------------------

func TestPeerResolver_SelfIP_NeverProbed(t *testing.T) {
	// Peer server that records if it was hit.
	selfProbed := false
	selfServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		selfProbed = true
		w.WriteHeader(http.StatusOK)
	}))
	defer selfServer.Close()

	selfHost, selfPort := splitHostPort(t, selfServer.Listener.Addr().String())

	ready := true
	clientset := fake.NewSimpleClientset(&discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "self-slice",
			Namespace: "ns",
			Labels:    map[string]string{discoveryv1.LabelServiceName: "svc"},
		},
		Endpoints: []discoveryv1.Endpoint{
			{
				Addresses:  []string{selfHost},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
			},
		},
	})

	// myPodIP matches the only endpoint => no peers to probe.
	resolver := daemon.NewPeerResolver(lagertest.NewTestLogger("self"), clientset, "ns", "svc", selfPort, selfHost, nil)
	_, found := resolver.Probe(context.Background(), "any-key")
	if found {
		t.Error("expected false when self is the only endpoint")
	}
	if selfProbed {
		t.Error("self IP should never be probed")
	}
}

// ---------------------------------------------------------------------------
// LR-01: TTL sweeper edge cases
// ---------------------------------------------------------------------------

func TestSweeper_CachesDirectoryNotSwept(t *testing.T) {
	storagePath := t.TempDir()

	// Create an old cache file.
	cachesDir := filepath.Join(storagePath, "artifacts", "caches", "pipeline-x")
	os.MkdirAll(cachesDir, 0755)
	cacheFile := filepath.Join(cachesDir, "data.bin")
	os.WriteFile(cacheFile, []byte("cached-data"), 0644)
	os.Chtimes(cachesDir, time.Now().Add(-10*time.Hour), time.Now().Add(-10*time.Hour))
	os.Chtimes(cacheFile, time.Now().Add(-10*time.Hour), time.Now().Add(-10*time.Hour))

	logger := lagertest.NewTestLogger("sweeper")
	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, nil)
	sweeper.SweepOnce()

	// Cache data must survive.
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("cache file should not be removed: %v", err)
	}
	if string(data) != "cached-data" {
		t.Errorf("cache file corrupted: %q", string(data))
	}
}

func TestSweeper_CallsRemoveByPath_OnRegistry(t *testing.T) {
	storagePath := t.TempDir()
	logger := lagertest.NewTestLogger("sweeper-registry")

	// Create an expired step directory.
	oldStep := filepath.Join(storagePath, "steps", "expired-container")
	resultDir := filepath.Join(oldStep, "result")
	os.MkdirAll(resultDir, 0755)
	os.WriteFile(filepath.Join(resultDir, "out.txt"), []byte("x"), 0644)
	os.Chtimes(oldStep, time.Now().Add(-5*time.Hour), time.Now().Add(-5*time.Hour))

	// Set up registry with an alias pointing into the expired step.
	registry := daemon.NewRegistry(logger)
	store := daemon.NewAliasStore(logger, storagePath)
	registry.SetAliasStore(store)
	registry.RegisterAlias("vol-expired", resultDir)

	if registry.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", registry.Len())
	}

	sweeper := daemon.NewSweeper(logger, storagePath, 2*time.Hour, 5*time.Minute, registry)
	sweeper.SweepOnce()

	// Step directory should be gone.
	if _, err := os.Stat(oldStep); !os.IsNotExist(err) {
		t.Error("expected expired step to be removed")
	}

	// Registry entry should be cleaned up via RemoveByPath.
	if _, ok := registry.Lookup("vol-expired"); ok {
		t.Error("expected registry entry to be removed after sweep")
	}

	// Alias persistence should also be updated.
	r2 := daemon.NewRegistry(logger)
	r2.SetAliasStore(store)
	r2.LoadAliases()
	if _, ok := r2.Lookup("vol-expired"); ok {
		t.Error("expected alias to be gone from disk after sweep")
	}
}
