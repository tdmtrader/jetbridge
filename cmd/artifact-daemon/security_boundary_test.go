package main_test

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boundaryRequest(t *testing.T, client *http.Client, method, target string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestSnapshotNamespaceIsReservedBeforeGenericArtifactDispatch(t *testing.T) {
	ts, storagePath := setupServer(t)
	content := []byte("sealed snapshot")
	digest := snapshotDigest(content)
	exactPath := filepath.Join(storagePath, "snapshots", "sha256", digest+".tar")
	if err := os.MkdirAll(filepath.Dir(exactPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exactPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/artifacts/snapshots"},
		{http.MethodHead, "/artifacts/snapshots/sha256"},
		{http.MethodPut, "/artifacts/snapshots/not-a-snapshot"},
		{http.MethodDelete, "/artifacts/snapshots/sha256/not-a-digest.tar"},
		{http.MethodPut, "/artifacts/snapshots//sha256/" + digest + ".tar"},
		{http.MethodDelete, "/artifacts/snapshots%2fsha256%2f" + digest + ".tar"},
		{http.MethodPut, "/artifacts/.artifact-daemon-staging/poison"},
	} {
		t.Run(tc.method+"_"+tc.path, func(t *testing.T) {
			client := *ts.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			resp := boundaryRequest(t, &client, tc.method, ts.URL+tc.path, strings.NewReader("poison"))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
		})
	}

	registerBody, _ := json.Marshal(map[string]string{"key": "alias", "local_path": exactPath})
	if got := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/register", bytes.NewReader(registerBody)).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("register snapshot alias status = %d, want 400", got)
	}

	stored, err := os.ReadFile(exactPath)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("reserved snapshot changed: %q, %v", stored, err)
	}
}

func TestArtifactKeysAcceptOnlyCanonicalUnicodePercentEncoding(t *testing.T) {
	ts, storagePath := setupServer(t)
	valid := boundaryRequest(t, ts.Client(), http.MethodPut, ts.URL+"/artifacts/steps/caf%C3%A9/output", strings.NewReader("unicode"))
	if valid.StatusCode != http.StatusCreated {
		t.Fatalf("canonical Unicode key status = %d, want 201", valid.StatusCode)
	}
	if got, err := os.ReadFile(filepath.Join(storagePath, "steps", "café", "output")); err != nil || string(got) != "unicode" {
		t.Fatalf("Unicode artifact = %q, %v", got, err)
	}

	for _, encoded := range []string{
		"steps/caf%c3%a9/other",
		"steps/%63afe/other",
		"steps/one%2Ftwo/other",
		"steps/%2e%2e/other",
		"steps/%ZZ/other",
	} {
		req, err := http.NewRequest(http.MethodPut, ts.URL+"/artifacts/"+encoded, strings.NewReader("poison"))
		if err != nil {
			continue // malformed URL was rejected even before the daemon boundary
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("noncanonical key %q status = %d, want 400", encoded, resp.StatusCode)
		}
	}
}

func TestSnapshotPUTHashesCandidateBeforeExistingComparison(t *testing.T) {
	ts, storagePath := setupServer(t)
	wrongPayload := []byte("wrong but identical to corrupt stored bytes")
	expectedDigest := strings.Repeat("0", 64)
	storedPath := filepath.Join(storagePath, "snapshots", "sha256", expectedDigest+".tar")
	if err := os.MkdirAll(filepath.Dir(storedPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storedPath, wrongPayload, 0644); err != nil {
		t.Fatal(err)
	}
	resp := boundaryRequest(t, ts.Client(), http.MethodPut,
		ts.URL+"/artifacts/snapshots/sha256/"+expectedDigest+".tar", bytes.NewReader(wrongPayload))
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 before idempotency comparison", resp.StatusCode)
	}
}

func TestStreamInRejectsEncodedTraversalAndSymlinkPivot(t *testing.T) {
	ts, storagePath := setupServer(t)
	content := []byte("sealed snapshot")
	digest := snapshotDigest(content)
	exactPath := filepath.Join(storagePath, "snapshots", "sha256", digest+".tar")
	if err := os.MkdirAll(filepath.Dir(exactPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exactPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	traversal := boundaryRequest(t, ts.Client(), http.MethodPut,
		ts.URL+"/stream-in/%2e%2e%2fsnapshots%2fsha256", strings.NewReader("not tar"))
	if traversal.StatusCode != http.StatusBadRequest {
		t.Fatalf("encoded traversal status = %d, want 400", traversal.StatusCode)
	}
	client := *ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	repeated := boundaryRequest(t, &client, http.MethodPut, ts.URL+"/stream-in/handle//output", strings.NewReader("not tar"))
	if repeated.StatusCode != http.StatusBadRequest {
		t.Fatalf("repeated-separator stream key status = %d, want 400", repeated.StatusCode)
	}

	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "pivot", Typeflag: tar.TypeSymlink, Linkname: "../../snapshots/sha256", Mode: 0777}); err != nil {
		t.Fatal(err)
	}
	poison := []byte("poison")
	if err := tw.WriteHeader(&tar.Header{Name: "pivot/" + digest + ".tar", Typeflag: tar.TypeReg, Mode: 0644, Size: int64(len(poison))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(poison); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	pivot := boundaryRequest(t, ts.Client(), http.MethodPut, ts.URL+"/stream-in/handle/output", bytes.NewReader(archive.Bytes()))
	if pivot.StatusCode != http.StatusBadRequest {
		t.Fatalf("symlink pivot status = %d, want 400", pivot.StatusCode)
	}

	stored, err := os.ReadFile(exactPath)
	if err != nil || !bytes.Equal(stored, content) {
		t.Fatalf("snapshot changed through stream-in: %q, %v", stored, err)
	}
}

func TestResolveRejectsUnconfinedKeysDestinationsAndMixedBatch(t *testing.T) {
	ts, storagePath := setupServer(t)
	source := filepath.Join(storagePath, "steps", "source", "out")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}
	snapshotParent := filepath.Join(storagePath, "snapshots")
	snapshotExact := filepath.Join(snapshotParent, "sha256", strings.Repeat("a", 64)+".tar")
	if err := os.MkdirAll(filepath.Dir(snapshotExact), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotExact, []byte("sealed"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, item := range []map[string]string{
		{"key": "../snapshots", "dest": filepath.Join(storagePath, "steps", "safe-dest")},
		{"key": "source/out", "dest": snapshotParent},
		{"key": "source/out", "dest": snapshotExact},
		{"key": "source/out", "dest": filepath.Join(storagePath, "steps-sibling", "dest")},
	} {
		body, _ := json.Marshal(item)
		resp := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve", bytes.NewReader(body))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("resolve %#v status = %d, want 400", item, resp.StatusCode)
		}
	}

	outside := t.TempDir()
	symlinkParent := filepath.Join(storagePath, "steps", "linked")
	if err := os.Symlink(outside, symlinkParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"key": "source/out", "dest": filepath.Join(symlinkParent, "dest")})
	if got := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve", bytes.NewReader(body)).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("symlink-parent resolve status = %d, want 400", got)
	}

	safeDest := filepath.Join(storagePath, "steps", "consumer", "input")
	batch, _ := json.Marshal(map[string]any{"items": []map[string]string{
		{"key": "source/out", "dest": safeDest},
		{"key": "source/out", "dest": snapshotExact},
	}})
	if got := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve-batch", bytes.NewReader(batch)).StatusCode; got != http.StatusBadRequest {
		t.Fatalf("mixed batch status = %d, want 400", got)
	}
	if _, err := os.Stat(safeDest); !os.IsNotExist(err) {
		t.Fatalf("mixed batch partially mutated safe destination: %v", err)
	}
	if got, err := os.ReadFile(snapshotExact); err != nil || string(got) != "sealed" {
		t.Fatalf("snapshot changed through resolve: %q, %v", got, err)
	}
}

func TestResolveBatchRejectsOverlappingDestinationsBeforeMutation(t *testing.T) {
	ts, storagePath := setupServer(t)
	source := filepath.Join(storagePath, "steps", "source", "out")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data"), []byte("safe"), 0644); err != nil {
		t.Fatal(err)
	}

	parentDest := filepath.Join(storagePath, "steps", "consumer")
	childDest := filepath.Join(parentDest, "input")
	body, _ := json.Marshal(map[string]any{"items": []map[string]string{
		{"key": "source/out", "dest": parentDest},
		{"key": "source/out", "dest": childDest},
	}})
	resp := boundaryRequest(t, ts.Client(), http.MethodPost, ts.URL+"/resolve-batch", bytes.NewReader(body))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("overlapping batch status = %d, want 400", resp.StatusCode)
	}
	if _, err := os.Stat(parentDest); !os.IsNotExist(err) {
		t.Fatalf("overlapping batch mutated destination before rejection: %v", err)
	}
}
