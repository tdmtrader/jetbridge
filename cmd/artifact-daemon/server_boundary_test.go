package main

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
)

func newBoundaryServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()

	storagePath := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("boundary"), storagePath, "node-a")
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return server, httpServer, storagePath
}

// Malformed or hostile archives are ordinary network input. Malformed streams
// fail closed; escaping entries are ignored and never leave the extraction root.
func TestStreamInRejectsMalformedArchivesAndIgnoresEscapingEntries(t *testing.T) {
	_, httpServer, storagePath := newBoundaryServer(t)

	var traversal bytes.Buffer
	tw := tar.NewWriter(&traversal)
	content := []byte("must stay contained")
	if err := tw.WriteHeader(&tar.Header{Name: "../../escaped.txt", Mode: 0o644, Size: int64(len(content))}); err != nil {
		t.Fatalf("write traversal header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatalf("write traversal body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close traversal tar: %v", err)
	}

	tests := []struct {
		name       string
		key        string
		body       []byte
		wantStatus int
	}{
		{name: "invalid gzip", key: "bad-gzip", body: []byte{0x1f, 0x8b, 0x00}, wantStatus: http.StatusBadRequest},
		{name: "invalid tar header", key: "bad-tar", body: bytes.Repeat([]byte("x"), 512), wantStatus: http.StatusBadRequest},
		{name: "path traversal entry", key: "contained", body: traversal.Bytes(), wantStatus: http.StatusCreated},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPut, httpServer.URL+"/stream-in/"+tt.key, bytes.NewReader(tt.body))
			if err != nil {
				t.Fatalf("create request: %v", err)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("stream in: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want %d: %s", resp.StatusCode, tt.wantStatus, body)
			}

			if tt.wantStatus != http.StatusCreated {
				if _, err := os.Stat(filepath.Join(storagePath, "steps", tt.key)); !os.IsNotExist(err) {
					t.Fatalf("rejected upload left a final artifact: %v", err)
				}
			}
		})
	}

	if _, err := os.Stat(filepath.Join(storagePath, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("traversal entry escaped the extraction root: %v", err)
	}
	containedRoot := filepath.Join(storagePath, "steps", "contained")
	entries, err := os.ReadDir(containedRoot)
	if err != nil {
		t.Fatalf("read contained artifact: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("ignored traversal entry left files in the artifact: %v", entries)
	}
}

func TestGetResourceCacheBoundaryStates(t *testing.T) {
	server, httpServer, storagePath := newBoundaryServer(t)

	flatPath := filepath.Join(storagePath, "legacy-cache.tar")
	if err := os.WriteFile(flatPath, []byte("legacy bytes"), 0o644); err != nil {
		t.Fatalf("write flat cache: %v", err)
	}
	server.Registry().RegisterAlias("flat", flatPath)

	resp, err := http.Get(httpServer.URL + "/resource-caches/flat")
	if err != nil {
		t.Fatalf("get flat cache: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatalf("read flat cache: %v", readErr)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "legacy bytes" {
		t.Fatalf("flat cache response = (%d, %q), want (200, %q)", resp.StatusCode, body, "legacy bytes")
	}
	if got := resp.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("flat cache content type = %q", got)
	}

	stalePath := filepath.Join(storagePath, "stale-cache")
	if err := os.WriteFile(stalePath, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale cache: %v", err)
	}
	server.Registry().RegisterAlias("stale", stalePath)
	if err := os.Remove(stalePath); err != nil {
		t.Fatalf("remove stale cache: %v", err)
	}

	for _, tc := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/resource-caches/stale", wantStatus: http.StatusNotFound},
		{path: "/resource-caches/missing", wantStatus: http.StatusNotFound},
		{path: "/resource-caches/", wantStatus: http.StatusBadRequest},
	} {
		resp, err := http.Get(httpServer.URL + tc.path)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.wantStatus {
			t.Errorf("GET %s returned %d, want %d", tc.path, resp.StatusCode, tc.wantStatus)
		}
	}
	if _, found := server.Registry().Lookup("stale"); found {
		t.Error("stale resource-cache alias remained registered after a GET miss")
	}
}

func TestPutArtifactPreservesDirectoryOnPathCollision(t *testing.T) {
	_, httpServer, storagePath := newBoundaryServer(t)

	collision := filepath.Join(storagePath, "collision")
	if err := os.MkdirAll(collision, 0o755); err != nil {
		t.Fatalf("create collision directory: %v", err)
	}
	marker := filepath.Join(collision, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write collision marker: %v", err)
	}

	req, err := http.NewRequest(http.MethodPut, httpServer.URL+"/artifacts/collision", strings.NewReader("replacement"))
	if err != nil {
		t.Fatalf("create PUT: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT collision: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("PUT over a directory returned %d, want 500", resp.StatusCode)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "keep" {
		t.Fatalf("failed PUT damaged existing directory: body=%q err=%v", body, err)
	}
	if matches, err := filepath.Glob(filepath.Join(storagePath, ".put-tmp-*")); err != nil || len(matches) != 0 {
		t.Fatalf("failed PUT left temporary files %v: %v", matches, err)
	}
}

func TestResolveReportsCopyFailure(t *testing.T) {
	server, httpServer, storagePath := newBoundaryServer(t)

	source := filepath.Join(storagePath, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write source: %v", err)
	}
	server.Registry().Register("registered", source)
	dest := filepath.Join(t.TempDir(), "missing-parent", "dest")
	payload, _ := json.Marshal(resolveRequest{Key: "registered", Dest: dest})
	resp, err := http.Post(httpServer.URL+"/resolve", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("resolve into missing parent returned %d, want 500: %s", resp.StatusCode, body)
	}
}

func TestRegisterRejectsMalformedJSONAndDurableKey(t *testing.T) {
	server, httpServer, storagePath := newBoundaryServer(t)

	resp, err := http.Post(httpServer.URL+"/register", "application/json", strings.NewReader("{"))
	if err != nil {
		t.Fatalf("register malformed JSON: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed register returned %d, want 400", resp.StatusCode)
	}

	localPath := filepath.Join(storagePath, "cache")
	if err := os.WriteFile(localPath, []byte("cache"), 0o644); err != nil {
		t.Fatalf("write local cache: %v", err)
	}
	payload, _ := json.Marshal(registerRequest{Key: "cache", LocalPath: localPath, DurableKey: "../escape"})
	resp, err = http.Post(httpServer.URL+"/register", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("register invalid durable key: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid durable key returned %d, want 400", resp.StatusCode)
	}
	if _, found := server.Registry().Lookup("cache"); found {
		t.Error("invalid durable key registered the rejected artifact")
	}
}
