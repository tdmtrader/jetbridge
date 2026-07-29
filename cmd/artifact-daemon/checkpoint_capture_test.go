package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
)

func TestCheckpointCapturePrepareIsRootedAndDoesNotWriteHangar(t *testing.T) {
	storage := t.TempDir()
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/dir/result", "result")
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/.concourse/session/cursor", "42")
	server := NewServer(lagertest.NewTestLogger("test"), storage, "node-a")
	durable := new(hangarfakes.FakeStore)
	server.SetHangarStore(durable)
	staging, cleanupStaging, err := server.checkpointStagingDirectory()
	if err != nil {
		t.Fatal(err)
	}
	probe, err := os.MkdirTemp(staging, "probe-")
	cleanupStaging()
	if err != nil {
		t.Fatalf("private staging %q: %v", staging, err)
	}
	_ = os.RemoveAll(probe)

	prepared := prepareCheckpoint(t, server, checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024})
	if prepared.Handle == "" || prepared.Digest == "" {
		t.Fatalf("prepare result = %#v", prepared)
	}
	if calls := durable.EnsureCalls(); len(calls) != 0 {
		t.Fatalf("prepare wrote Hangar: %#v", calls)
	}
	poison := httptest.NewRecorder()
	server.Handler().ServeHTTP(poison, httptest.NewRequest(http.MethodPut, "/artifacts/.artifact-daemon-staging/poison", bytes.NewBufferString("poison")))
	if poison.Code != http.StatusBadRequest {
		t.Fatalf("generic staging poison status = %d, want 400", poison.Code)
	}

	for _, bad := range []checkpoint.ArchiveRequest{
		{ContainerHandle: "../agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024},
		{ContainerHandle: "agent-42", WorkspaceRoots: []string{"/tmp"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024},
	} {
		request := checkpointJSONRequest(t, http.MethodPost, "/checkpoints/v1/prepare", bad)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe prepare status = %d, want 400", response.Code)
		}
	}
}

func TestCheckpointCaptureUploadRequiresExactTicketAndConsumesOnce(t *testing.T) {
	storage := t.TempDir()
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/dir/result", "result")
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/.concourse/session/cursor", "42")
	server := NewServer(lagertest.NewTestLogger("test"), storage, "node-a")
	durable := new(hangarfakes.FakeStore)
	server.SetHangarStore(durable)
	prepared := prepareCheckpoint(t, server, checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024})

	badTicket := checkpoint.ObjectUploadTicket{ObjectID: 1, StagedCheckpointID: 2, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: "wrong", UploadToken: "token"}
	response := uploadCheckpoint(t, server, prepared.Handle, badTicket)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong ticket status = %d, want 403", response.Code)
	}
	if len(durable.EnsureCalls()) != 0 {
		t.Fatal("wrong ticket reached Hangar")
	}

	key, err := hangar.Key(hangar.KindCheckpoint, prepared.Digest)
	if err != nil {
		t.Fatal(err)
	}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 1, StagedCheckpointID: 2, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, UploadToken: "token"}
	durable.EnsureReturns(hangar.Attributes{Ref: hangar.ObjectRef{Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, Generation: 7}, UncompressedBytes: prepared.Bytes, CompressedBytes: 100}, nil)
	response = uploadCheckpoint(t, server, prepared.Handle, ticket)
	if response.Code != http.StatusOK {
		t.Fatalf("upload status = %d: %s", response.Code, response.Body.String())
	}
	if len(durable.EnsureCalls()) != 1 {
		t.Fatalf("Ensure calls = %d, want 1", len(durable.EnsureCalls()))
	}
	if replay := uploadCheckpoint(t, server, prepared.Handle, ticket); replay.Code != http.StatusNotFound {
		t.Fatalf("replay status = %d, want 404", replay.Code)
	}

	prepared = prepareCheckpoint(t, server, checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024})
	key, err = hangar.Key(hangar.KindCheckpoint, prepared.Digest)
	if err != nil {
		t.Fatal(err)
	}
	ticket = checkpoint.ObjectUploadTicket{ObjectID: 3, StagedCheckpointID: 4, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, UploadToken: "token"}
	durable.EnsureReturns(hangar.Attributes{Ref: hangar.ObjectRef{Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, Generation: 8}, UncompressedBytes: prepared.Bytes - 1, CompressedBytes: 1}, nil)
	if response := uploadCheckpoint(t, server, prepared.Handle, ticket); response.Code != http.StatusBadGateway {
		t.Fatalf("accounting mismatch status = %d, want 502", response.Code)
	}
}

func TestCheckpointCaptureUploadAlreadyAvailableAndExpiry(t *testing.T) {
	storage := t.TempDir()
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/dir/result", "result")
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/.concourse/session/cursor", "42")
	server := NewServer(lagertest.NewTestLogger("test"), storage, "node-a")
	if err := server.ConfigureCheckpointCapture(1024, 16, 1, 1<<20, 5*time.Millisecond, nil); err != nil {
		t.Fatal(err)
	}
	prepared := prepareCheckpoint(t, server, checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024})
	key, err := hangar.Key(hangar.KindCheckpoint, prepared.Digest)
	if err != nil {
		t.Fatal(err)
	}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 1, StagedCheckpointID: 2, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, AlreadyAvailable: true, AvailableGeneration: 9}
	if response := uploadCheckpoint(t, server, prepared.Handle, ticket); response.Code != http.StatusOK {
		t.Fatalf("already-available status = %d", response.Code)
	}

	prepared = prepareCheckpoint(t, server, checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024})
	time.Sleep(20 * time.Millisecond)
	if response := uploadCheckpoint(t, server, prepared.Handle, ticket); response.Code != http.StatusNotFound {
		t.Fatalf("expired upload status = %d, want 404", response.Code)
	}
}

func TestCheckpointCaptureRejectsStagingOverflowAndHangarFailureIsOneShot(t *testing.T) {
	storage := t.TempDir()
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/dir/result", "result")
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/.concourse/session/cursor", "42")
	server := NewServer(lagertest.NewTestLogger("test"), storage, "node-a")
	if err := server.ConfigureCheckpointCapture(1024, 16, 1, 1<<20, time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	durable := new(hangarfakes.FakeStore)
	durable.EnsureReturns(hangar.Attributes{}, errors.New("Hangar unavailable"))
	server.SetHangarStore(durable)
	request := checkpoint.ArchiveRequest{ContainerHandle: "agent-42", WorkspaceRoots: []string{"dir"}, SessionRoots: []string{".concourse/session"}, MaxBytes: 1024}
	prepared := prepareCheckpoint(t, server, request)
	overflow := httptest.NewRecorder()
	server.Handler().ServeHTTP(overflow, checkpointJSONRequest(t, http.MethodPost, "/checkpoints/v1/prepare", request))
	if overflow.Code != http.StatusTooManyRequests {
		t.Fatalf("staging overflow status = %d, want 429", overflow.Code)
	}
	key, err := hangar.Key(hangar.KindCheckpoint, prepared.Digest)
	if err != nil {
		t.Fatal(err)
	}
	ticket := checkpoint.ObjectUploadTicket{ObjectID: 1, StagedCheckpointID: 2, Kind: hangar.KindCheckpoint, Digest: prepared.Digest, Key: key, UploadToken: "token"}
	if response := uploadCheckpoint(t, server, prepared.Handle, ticket); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("Hangar failure status = %d, want 503", response.Code)
	}
	if response := uploadCheckpoint(t, server, prepared.Handle, ticket); response.Code != http.StatusNotFound {
		t.Fatalf("Hangar failure replay = %d, want 404", response.Code)
	}
}

func TestCheckpointCaptureRouteUsesExistingTLSProtection(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("test"), t.TempDir(), "node-a")
	response := httptest.NewRecorder()
	server.Handler(WithTLS()).ServeHTTP(response, checkpointJSONRequest(t, http.MethodPost, "/checkpoints/v1/prepare", checkpoint.ArchiveRequest{}))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("TLS-protected checkpoint route status = %d, want 401", response.Code)
	}
}

func prepareCheckpoint(t *testing.T, server *Server, body checkpoint.ArchiveRequest) checkpoint.PreparedArchive {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, checkpointJSONRequest(t, http.MethodPost, "/checkpoints/v1/prepare", body))
	if response.Code != http.StatusCreated {
		t.Fatalf("prepare status = %d: %s", response.Code, response.Body.String())
	}
	var prepared checkpoint.PreparedArchive
	if err := json.NewDecoder(response.Body).Decode(&prepared); err != nil {
		t.Fatal(err)
	}
	return prepared
}

func uploadCheckpoint(t *testing.T, server *Server, handle string, ticket checkpoint.ObjectUploadTicket) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, checkpointJSONRequest(t, http.MethodPost, "/checkpoints/v1/upload/"+handle, ticket))
	return response
}
func checkpointJSONRequest(t *testing.T, method, target string, value any) *http.Request {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}
func writeCheckpointCaptureFile(t *testing.T, root, relative, content string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
