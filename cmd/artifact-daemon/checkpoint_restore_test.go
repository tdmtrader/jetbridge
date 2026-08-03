package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/checkpoint"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestCheckpointRestoreRouteUsesTLSProtection(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("restore"), t.TempDir(), "node-a")
	response := httptest.NewRecorder()
	server.Handler(WithTLS()).ServeHTTP(response, checkpointJSONRequest(t, http.MethodPost, "/checkpoints/v1/restore", checkpoint.RestoreRequest{}))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("TLS-protected restore status = %d, want 401", response.Code)
	}
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/artifacts/.checkpoint-restore-gates/agent-42/marker/ready", bytes.NewBufferString("poison")))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("generic restore-gate write status = %d, want 400", response.Code)
	}
}

func TestCheckpointRestoreTopologyRejectsExtraRootsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"workspace", "session"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("a", 64)), 1)
	if err != nil {
		t.Fatal(err)
	}
	request := checkpoint.RestoreRequest{ContainerHandle: "agent-42", MaterializationID: "materialization-2", PodUID: "pod-uid-2", Archive: checkpoint.Archive{Ref: ref}, WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{"session"}, MaxBytes: 1024, MaxEntries: 16}
	if err := validateCheckpointRestoreTopology(root, request); err != nil {
		t.Fatalf("valid topology: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "extra"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateCheckpointRestoreTopology(root, request); err == nil {
		t.Fatal("extra top-level root succeeded")
	}
	if err := os.Remove(filepath.Join(root, "extra")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "workspace", "link")); err != nil {
		t.Fatal(err)
	}
	if err := validateCheckpointRestoreTopology(root, request); err == nil {
		t.Fatal("symlink checkpoint entry succeeded")
	}
}

func TestCheckpointRestoreTopologyRejectsUndeclaredMultiRoot(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"workspace", "session", "workspace/alpha", "workspace/beta"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	request := checkpointRestoreTestRequest(t)
	request.WorkspaceRoots = []string{"alpha", "beta"}
	if err := validateCheckpointRestoreTopology(root, request); err != nil {
		t.Fatalf("declared multi-root topology: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "workspace", "undeclared"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateCheckpointRestoreTopology(root, request); err == nil {
		t.Fatal("undeclared multi-root checkpoint content succeeded")
	}
}

func TestCheckpointSessionModeAppliesGroupReadWriteExecuteCorrectly(t *testing.T) {
	for name, test := range map[string]struct {
		mode      uint32
		directory bool
		want      uint32
	}{
		"directory":                   {mode: 0700, directory: true, want: 0770},
		"non-executable regular file": {mode: 0600, want: 0660},
		"executable regular file":     {mode: 0700, want: 0770},
	} {
		t.Run(name, func(t *testing.T) {
			if got := checkpointSessionMode(test.mode, test.directory); got != test.want {
				t.Fatalf("checkpointSessionMode(%#o, %t) = %#o, want %#o", test.mode, test.directory, got, test.want)
			}
		})
	}
}

func TestCheckpointRestoreMarkerIsPrivateAndBoundToHandleMaterializationAndUID(t *testing.T) {
	storage := t.TempDir()
	server := NewServer(lagertest.NewTestLogger("restore-marker"), storage, "node-a")
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("b", 64)), 2)
	if err != nil {
		t.Fatal(err)
	}
	marker := checkpointRestoreMarker{MaterializationID: "materialization-2", RequestHash: strings.Repeat("a", 64), PodUID: "pod-uid-2", Object: hangar.Attributes{Ref: ref, CompressedBytes: 100, UncompressedBytes: 200}}
	precreateCheckpointGate(t, storage, checkpoint.RestoreRequest{ContainerHandle: "agent-42", MaterializationID: marker.MaterializationID})
	if err := server.writeCheckpointRestoreMarker("agent-42", marker.MaterializationID, marker); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(storage, checkpointRestoreGatesDirectory, "agent-42", checkpointRestoreMarkerName(marker.MaterializationID), "ready")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker path %q: %v", path, err)
	}
	got, found, err := server.readCheckpointRestoreMarker("agent-42", marker.MaterializationID)
	if err != nil || !found || got != marker {
		t.Fatalf("read marker = %#v, %t, %v", got, found, err)
	}
	replacement := marker
	replacement.PodUID = "other-pod"
	if err := server.writeCheckpointRestoreMarker("agent-42", marker.MaterializationID, replacement); !errors.Is(err, os.ErrExist) {
		t.Fatalf("replace published marker error = %v, want file exists", err)
	}
	got, found, err = server.readCheckpointRestoreMarker("agent-42", marker.MaterializationID)
	if err != nil || !found || got != marker {
		t.Fatalf("marker after no-replace publication = %#v, %t, %v", got, found, err)
	}
	if _, found, err := server.readCheckpointRestoreMarker("agent-other", marker.MaterializationID); err != nil || found {
		t.Fatalf("other handle marker = %t, %v", found, err)
	}
}

func TestCheckpointRestoreRequiresPrecreatedGateBeforeOpeningHangar(t *testing.T) {
	server, durable, request, _, _ := checkpointRestoreFixture(t)
	if _, err := server.restoreCheckpoint(context.Background(), request); err == nil {
		t.Fatal("missing gate leaf restored checkpoint")
	}
	if calls := durable.OpenCalls(); len(calls) != 0 {
		t.Fatalf("missing gate reached Hangar: %#v", calls)
	}
}

func TestCheckpointRestoreVerificationReadsOnlyAnExactExistingMarker(t *testing.T) {
	server, durable, request, storage, _ := checkpointRestoreFixture(t)
	precreateCheckpointGate(t, storage, request)
	if _, err := server.verifyCheckpointRestore(request); err == nil {
		t.Fatal("missing marker verified")
	}
	if calls := durable.OpenCalls(); len(calls) != 0 {
		t.Fatalf("verification opened Hangar without marker: %#v", calls)
	}
	hash, err := checkpointRestoreRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	marker := checkpointRestoreMarker{MaterializationID: request.MaterializationID, RequestHash: hash, PodUID: request.PodUID, Object: hangar.Attributes{Ref: request.Archive.Ref, CompressedBytes: 1, UncompressedBytes: 1024}}
	if err := server.writeCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID, marker); err != nil {
		t.Fatal(err)
	}
	if _, err := server.verifyCheckpointRestore(request); err != nil {
		t.Fatalf("exact marker verification: %v", err)
	}
	if calls := durable.OpenCalls(); len(calls) != 0 {
		t.Fatalf("verification opened Hangar: %#v", calls)
	}
}

func TestCheckpointRestoreCopiesExactCanonicalArchivePreservingRootsAndReplaysMarker(t *testing.T) {
	server, durable, request, storage, _ := checkpointRestoreFixture(t)
	precreateCheckpointGate(t, storage, request)
	workspace := filepath.Join(storage, "steps", request.ContainerHandle, "workspace")
	session := filepath.Join(storage, "steps", request.ContainerHandle, "session")
	workspaceInfo, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	sessionInfo, err := os.Stat(session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.restoreCheckpoint(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(workspace, "fresh")); err != nil || string(content) != "workspace" {
		t.Fatalf("restored workspace = %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "stale")); !os.IsNotExist(err) {
		t.Fatalf("stale workspace content survived: %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(session, "cursor")); err != nil || string(content) != "session" {
		t.Fatalf("restored session = %q, %v", content, err)
	}
	workspaceAfter, err := os.Stat(workspace)
	if err != nil {
		t.Fatal(err)
	}
	sessionAfter, err := os.Stat(session)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(workspaceInfo, workspaceAfter) || !os.SameFile(sessionInfo, sessionAfter) {
		t.Fatal("restore replaced a pre-created hostPath root inode")
	}
	if _, found, err := server.readCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID); err != nil || !found {
		t.Fatalf("success marker = %t, %v", found, err)
	}
	if _, err := server.restoreCheckpoint(context.Background(), request); err != nil {
		t.Fatalf("exact marker replay: %v", err)
	}
	if calls := durable.OpenCalls(); len(calls) != 1 {
		t.Fatalf("exact marker replay reopened Hangar %d times", len(calls))
	}
	changed := request.Clone()
	changed.PodUID = "other-pod"
	if _, err := server.restoreCheckpoint(context.Background(), changed); err == nil {
		t.Fatal("Pod UID mismatch replay succeeded")
	}
	if calls := durable.OpenCalls(); len(calls) != 1 {
		t.Fatalf("mismatch replay reopened Hangar %d times", len(calls))
	}
}

func TestCheckpointRestoreReplaysAfterInterruptedMarkerPublication(t *testing.T) {
	for name, contents := range map[string]string{
		"empty marker": "",
		"truncated marker after both complete identifiers": `{"materialization_id":"materialization-2","request_hash":"` + strings.Repeat("a", 64) + `","object":{},"pod_uid":"pod-uid-2"`,
	} {
		t.Run(name, func(t *testing.T) {
			server, durable, request, storage, _ := checkpointRestoreFixture(t)
			precreateCheckpointGate(t, storage, request)
			markerPath := filepath.Join(storage, checkpointRestoreGatesDirectory, request.ContainerHandle, checkpointRestoreMarkerName(request.MaterializationID), "ready")
			if err := os.WriteFile(markerPath, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}

			if _, err := server.restoreCheckpoint(context.Background(), request); err != nil {
				t.Fatalf("restore after interrupted marker publication: %v", err)
			}
			if calls := durable.OpenCalls(); len(calls) != 1 {
				t.Fatalf("restore after interrupted marker opened Hangar %d times, want 1", len(calls))
			}
			if _, found, err := server.readCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID); err != nil || !found {
				t.Fatalf("replayed restore marker = %t, %v", found, err)
			}
		})
	}
}

func TestCheckpointRestoreDoesNotRemoveUnsafeMarkerEntry(t *testing.T) {
	server, durable, request, storage, _ := checkpointRestoreFixture(t)
	precreateCheckpointGate(t, storage, request)
	external := filepath.Join(t.TempDir(), "external")
	if err := os.WriteFile(external, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(storage, checkpointRestoreGatesDirectory, request.ContainerHandle, checkpointRestoreMarkerName(request.MaterializationID), "ready")
	if err := os.Symlink(external, markerPath); err != nil {
		t.Fatal(err)
	}

	if _, err := server.restoreCheckpoint(context.Background(), request); err == nil {
		t.Fatal("restore removed an unsafe marker entry")
	}
	if calls := durable.OpenCalls(); len(calls) != 0 {
		t.Fatalf("unsafe marker reached Hangar: %#v", calls)
	}
	info, err := os.Lstat(markerPath)
	if err != nil {
		t.Fatalf("unsafe marker was removed: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("unsafe marker type changed: mode=%v", info.Mode())
	}
	if contents, err := os.ReadFile(external); err != nil || string(contents) != "untouched" {
		t.Fatalf("external marker target = %q, %v", contents, err)
	}
}

func TestCheckpointRestoreDoesNotRemoveNonRegularMarkerEntry(t *testing.T) {
	server, durable, request, storage, _ := checkpointRestoreFixture(t)
	precreateCheckpointGate(t, storage, request)
	markerPath := filepath.Join(storage, checkpointRestoreGatesDirectory, request.ContainerHandle, checkpointRestoreMarkerName(request.MaterializationID), "ready")
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := server.restoreCheckpoint(context.Background(), request); err == nil {
		t.Fatal("restore removed a non-regular marker entry")
	}
	if calls := durable.OpenCalls(); len(calls) != 0 {
		t.Fatalf("non-regular marker reached Hangar: %#v", calls)
	}
	if info, err := os.Lstat(markerPath); err != nil || !info.IsDir() {
		t.Fatalf("non-regular marker changed: info=%v err=%v", info, err)
	}
}

func TestCheckpointRestoreFailureLeavesNoMarkerAndCanRetrySameGate(t *testing.T) {
	server, durable, request, storage, _ := checkpointRestoreFixture(t)
	precreateCheckpointGate(t, storage, request)
	server.normalizeCheckpointSession = func(*os.File) error { return os.ErrPermission }
	if _, err := server.restoreCheckpoint(context.Background(), request); err == nil {
		t.Fatal("injected session normalization failure succeeded")
	}
	if _, found, err := server.readCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID); err != nil || found {
		t.Fatalf("failed restore marker = %t, %v", found, err)
	}
	server.normalizeCheckpointSession = func(*os.File) error { return nil }
	if _, err := server.restoreCheckpoint(context.Background(), request); err != nil {
		t.Fatalf("same gated retry: %v", err)
	}
	if calls := durable.OpenCalls(); len(calls) != 2 {
		t.Fatalf("restore retries = %d, want 2", len(calls))
	}
}

func TestCheckpointRestoreRejectsMismatchedArchiveDigestAndAccounting(t *testing.T) {
	for name, configure := range map[string]func(*hangarfakes.FakeStore, checkpoint.RestoreRequest, []byte){
		"wrong object reference": func(durable *hangarfakes.FakeStore, request checkpoint.RestoreRequest, raw []byte) {
			wrongRef, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("d", 64)), request.Archive.Ref.Generation)
			if err != nil {
				t.Fatal(err)
			}
			durable.SetOpenStub(func(context.Context, hangar.ObjectRef, int64) (io.ReadCloser, hangar.Attributes, error) {
				return io.NopCloser(bytes.NewReader(raw)), hangar.Attributes{Ref: wrongRef, CompressedBytes: 1, UncompressedBytes: int64(len(raw))}, nil
			})
		},
		"wrong canonical digest": func(durable *hangarfakes.FakeStore, request checkpoint.RestoreRequest, raw []byte) {
			changed := append([]byte(nil), raw...)
			contentOffset := bytes.LastIndex(changed, []byte("workspace"))
			if contentOffset < 0 {
				t.Fatal("canonical fixture has no workspace content")
			}
			copy(changed[contentOffset:], "different")
			durable.SetOpenStub(func(context.Context, hangar.ObjectRef, int64) (io.ReadCloser, hangar.Attributes, error) {
				return io.NopCloser(bytes.NewReader(changed)), hangar.Attributes{Ref: request.Archive.Ref, CompressedBytes: 1, UncompressedBytes: int64(len(changed))}, nil
			})
		},
		"wrong canonical accounting": func(durable *hangarfakes.FakeStore, request checkpoint.RestoreRequest, raw []byte) {
			durable.SetOpenStub(func(context.Context, hangar.ObjectRef, int64) (io.ReadCloser, hangar.Attributes, error) {
				return io.NopCloser(bytes.NewReader(raw)), hangar.Attributes{Ref: request.Archive.Ref, CompressedBytes: 1, UncompressedBytes: int64(len(raw)) + 1}, nil
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, durable, request, storage, raw := checkpointRestoreFixture(t)
			precreateCheckpointGate(t, storage, request)
			configure(durable, request, raw)
			if _, err := server.restoreCheckpoint(context.Background(), request); err == nil {
				t.Fatal("mismatched archive restored")
			}
			if _, found, err := server.readCheckpointRestoreMarker(request.ContainerHandle, request.MaterializationID); err != nil || found {
				t.Fatalf("rejected archive marker = %t, %v", found, err)
			}
		})
	}
}

func checkpointRestoreFixture(t *testing.T) (*Server, *hangarfakes.FakeStore, checkpoint.RestoreRequest, string, []byte) {
	t.Helper()
	source := t.TempDir()
	writeCheckpointCaptureFile(t, source, "workspace/fresh", "workspace")
	writeCheckpointCaptureFile(t, source, "session/cursor", "session")
	captured, err := checkpoint.CaptureArchive(context.Background(), checkpoint.ArchiveRequest{ContainerHandle: "source", WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{"session"}, MaxBytes: 1 << 20}, checkpoint.ArchiveSource{ContainerRoot: source, MaxBytes: 1 << 20, MaxEntries: 32, TempDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer captured.Close()
	archive, err := captured.Open()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(archive)
	closeErr := archive.Close()
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, captured.Prepared.Digest, 7)
	if err != nil {
		t.Fatal(err)
	}
	storage := t.TempDir()
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/workspace/stale", "stale")
	writeCheckpointCaptureFile(t, storage, "steps/agent-42/session/stale", "stale")
	durable := new(hangarfakes.FakeStore)
	logicalMaxBytes := int64(64)
	archiveByteLimit, err := snapshot.CanonicalArchiveByteLimit(logicalMaxBytes, 32)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) <= logicalMaxBytes {
		t.Fatalf("canonical tar size = %d, want more than logical limit %d", len(raw), logicalMaxBytes)
	}
	durable.SetOpenStub(func(_ context.Context, got hangar.ObjectRef, max int64) (io.ReadCloser, hangar.Attributes, error) {
		if got != ref || max != archiveByteLimit {
			t.Fatalf("Hangar Open = %#v, %d", got, max)
		}
		return io.NopCloser(bytes.NewReader(raw)), hangar.Attributes{Ref: ref, CompressedBytes: 100, UncompressedBytes: int64(len(raw))}, nil
	})
	server := NewServer(lagertest.NewTestLogger("restore"), storage, "node-a")
	server.SetHangarStore(durable)
	server.normalizeCheckpointSession = func(*os.File) error { return nil }
	request := checkpoint.RestoreRequest{ContainerHandle: "agent-42", MaterializationID: "materialization-2", PodUID: "pod-uid-2", Archive: checkpoint.Archive{Ref: ref}, WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{"session"}, MaxBytes: logicalMaxBytes, MaxEntries: 32}
	return server, durable, request, storage, raw
}

func precreateCheckpointGate(t *testing.T, storage string, request checkpoint.RestoreRequest) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(storage, checkpointRestoreGatesDirectory, request.ContainerHandle, checkpointRestoreMarkerName(request.MaterializationID)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func checkpointRestoreTestRequest(t *testing.T) checkpoint.RestoreRequest {
	t.Helper()
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat("c", 64)), 1)
	if err != nil {
		t.Fatal(err)
	}
	return checkpoint.RestoreRequest{ContainerHandle: "agent-42", MaterializationID: "materialization-2", PodUID: "pod-uid-2", Archive: checkpoint.Archive{Ref: ref}, WorkspaceRoots: []string{"workspace"}, SessionRoots: []string{"session"}, MaxBytes: 1024, MaxEntries: 16}
}
