package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestDefaultSnapshotDaemonCeilingMatchesCanonicalizerBound(t *testing.T) {
	want, err := snapshot.CanonicalArchiveByteLimit(snapshot.DefaultMaxSnapshotContentBytes, snapshot.DefaultMaxSnapshotEntries)
	if err != nil {
		t.Fatal(err)
	}
	if defaultSnapshotMaxBytes != want {
		t.Fatalf("daemon ceiling = %d, want shared default bound %d", defaultSnapshotMaxBytes, want)
	}
}

func internalSnapshotPath(storagePath string, content []byte) (string, string) {
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	return "/artifacts/snapshots/sha256/" + digest + ".tar", filepath.Join(storagePath, "snapshots", "sha256", digest+".tar")
}

func serveSnapshotRequest(server *Server, method, target string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, body)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	return response
}

func TestSnapshotIdenticalPUTRetriesFailedParentSync(t *testing.T) {
	storagePath := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("snapshot-sync"), storagePath, "node")
	content := []byte("canonical content")
	target, _ := internalSnapshotPath(storagePath, content)
	if got := serveSnapshotRequest(server, http.MethodPut, target, bytes.NewReader(content)).Code; got != http.StatusCreated {
		t.Fatalf("initial PUT = %d", got)
	}

	fail := true
	server.syncSnapshotDirectory = func(root *os.Root, name string) error {
		if fail {
			fail = false
			return errors.New("injected directory fsync failure")
		}
		return syncRootDirectory(root, name)
	}
	if got := serveSnapshotRequest(server, http.MethodPut, target, bytes.NewReader(content)).Code; got != http.StatusInternalServerError {
		t.Fatalf("identical PUT with failed fsync = %d, want 500", got)
	}
	if got := serveSnapshotRequest(server, http.MethodPut, target, bytes.NewReader(content)).Code; got != http.StatusOK {
		t.Fatalf("identical PUT retry = %d, want 200", got)
	}
}

func TestSnapshotFirstPUTFsyncsNamespaceParentsInOrder(t *testing.T) {
	storagePath := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("snapshot-namespace-sync"), storagePath, "node")
	var synced []string
	server.syncSnapshotDirectory = func(root *os.Root, name string) error {
		synced = append(synced, name)
		return nil
	}
	content := []byte("first durable content")
	target, _ := internalSnapshotPath(storagePath, content)
	if got := serveSnapshotRequest(server, http.MethodPut, target, bytes.NewReader(content)).Code; got != http.StatusCreated {
		t.Fatalf("PUT = %d, want 201", got)
	}
	want := []string{".", "snapshots", "snapshots/sha256", "snapshots/sha256"}
	if len(synced) != len(want) {
		t.Fatalf("fsync sequence = %v, want %v", synced, want)
	}
	for i := range want {
		if synced[i] != want[i] {
			t.Fatalf("fsync sequence = %v, want %v", synced, want)
		}
	}
}

func TestSnapshotMissingDELETERetriesFailedParentSync(t *testing.T) {
	storagePath := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("snapshot-delete-sync"), storagePath, "node")
	content := []byte("never stored")
	target, storedPath := internalSnapshotPath(storagePath, content)
	if err := os.MkdirAll(filepath.Dir(storedPath), 0755); err != nil {
		t.Fatal(err)
	}

	fail := true
	server.syncSnapshotDirectory = func(root *os.Root, name string) error {
		if fail {
			fail = false
			return errors.New("injected directory fsync failure")
		}
		return syncRootDirectory(root, name)
	}
	if got := serveSnapshotRequest(server, http.MethodDelete, target, nil).Code; got != http.StatusInternalServerError {
		t.Fatalf("missing DELETE with failed fsync = %d, want 500", got)
	}
	if got := serveSnapshotRequest(server, http.MethodDelete, target, nil).Code; got != http.StatusNoContent {
		t.Fatalf("missing DELETE retry = %d, want 204", got)
	}
}

type cancelWithFinalRead struct {
	data   []byte
	cancel context.CancelFunc
}

func (reader *cancelWithFinalRead) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, reader.data)
	reader.data = reader.data[n:]
	if len(reader.data) == 0 {
		reader.cancel()
		return n, io.EOF
	}
	return n, nil
}

func TestSnapshotExistingComparisonObservesCancellation(t *testing.T) {
	storagePath := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("snapshot-compare-cancel"), storagePath, "node")
	content := bytes.Repeat([]byte("x"), 256*1024)
	target, storedPath := internalSnapshotPath(storagePath, content)
	if err := os.MkdirAll(filepath.Dir(storedPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storedPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	body := &cancelWithFinalRead{data: append([]byte(nil), content...), cancel: cancel}
	req := httptest.NewRequest(http.MethodPut, target, io.NopCloser(body)).WithContext(ctx)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("canceled existing comparison = %d, want 500", response.Code)
	}
}
