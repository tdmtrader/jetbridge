package jetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/hangar"
)

func checkpointObjectTestRef(t *testing.T, seed string, generation int64) hangar.ObjectRef {
	t.Helper()
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, hangar.Digest("sha256:"+strings.Repeat(seed, 64)), generation)
	if err != nil {
		t.Fatalf("checkpoint object ref: %v", err)
	}
	return ref
}

func TestCheckpointObjectStoreDeletesThroughAnyLiveDaemon(t *testing.T) {
	ref := checkpointObjectTestRef(t, "a", 11)
	var seen []string
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-b", Address: "10.0.0.2"}, {NodeName: "node-a", Address: "10.0.0.1"}},
		func(request *http.Request) *http.Response {
			seen = append(seen, request.URL.Host+request.URL.Path)
			return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
		}))

	if err := store.DeleteObject(context.Background(), ref); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if len(seen) != 1 || seen[0] != "10.0.0.1:7780/checkpoints/v1/objects/delete" {
		t.Fatalf("unexpected requests: %v", seen)
	}
}

func TestCheckpointObjectStoreSendsTheExactPinnedReference(t *testing.T) {
	ref := checkpointObjectTestRef(t, "b", 11)
	var sent hangar.ObjectRef
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(request *http.Request) *http.Response {
			body, _ := io.ReadAll(request.Body)
			if err := decodeCheckpointObjectTestJSON(body, &sent); err != nil {
				t.Fatalf("decode delete body: %v", err)
			}
			return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
		}))

	if err := store.DeleteObject(context.Background(), ref); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if sent != ref {
		t.Fatalf("sent = %+v, want %+v", sent, ref)
	}
}

func TestCheckpointObjectStoreReportsAGenerationConflictAsAConflict(t *testing.T) {
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(*http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusConflict, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("changed"))}
		}))

	err := store.DeleteObject(context.Background(), checkpointObjectTestRef(t, "c", 11))
	if !errors.Is(err, hangar.ErrConflict) {
		t.Fatalf("DeleteObject = %v, want a hangar conflict", err)
	}
}

func TestCheckpointObjectStoreRefusesToDeleteAnythingButACheckpoint(t *testing.T) {
	snapshotRef, err := hangar.NewObjectRef(hangar.KindSnapshot, hangar.Digest("sha256:"+strings.Repeat("d", 64)), 11)
	if err != nil {
		t.Fatalf("snapshot ref: %v", err)
	}
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(*http.Request) *http.Response { t.Fatal("a foreign object reached the daemon"); return nil }))

	if err := store.DeleteObject(context.Background(), snapshotRef); err == nil {
		t.Fatal("expected a foreign-kind delete to be refused")
	}
}

func TestCheckpointObjectStoreInspectReturnsTheStoredGeneration(t *testing.T) {
	ref := checkpointObjectTestRef(t, "e", 17)
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(request *http.Request) *http.Response {
			if request.URL.Path != "/checkpoints/v1/objects/inspect" {
				t.Fatalf("path = %s", request.URL.Path)
			}
			return checkpointJSONResponse(http.StatusOK, ref)
		}))

	got, err := store.InspectObject(context.Background(), hangar.KindCheckpoint, ref.Digest)
	if err != nil {
		t.Fatalf("InspectObject: %v", err)
	}
	if got != ref {
		t.Fatalf("InspectObject = %+v, want %+v", got, ref)
	}
}

func TestCheckpointObjectStoreInspectRejectsAResponseAboutAnotherObject(t *testing.T) {
	requested := checkpointObjectTestRef(t, "e", 17)
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(*http.Request) *http.Response {
			return checkpointJSONResponse(http.StatusOK, checkpointObjectTestRef(t, "f", 17))
		}))

	if _, err := store.InspectObject(context.Background(), hangar.KindCheckpoint, requested.Digest); err == nil {
		t.Fatal("expected a mismatched inspect response to be refused")
	}
}

func TestCheckpointObjectStoreInspectReportsAProvenAbsence(t *testing.T) {
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(*http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("not found"))}
		}))

	_, err := store.InspectObject(context.Background(), hangar.KindCheckpoint, checkpointObjectTestRef(t, "e", 17).Digest)
	if !errors.Is(err, hangar.ErrNotFound) {
		t.Fatalf("InspectObject = %v, want hangar.ErrNotFound", err)
	}
}

// An unconfigured or unreachable daemon must never be read as absence: acting
// on absence retires the database row that is the object's last name.
func TestCheckpointObjectStoreNeverReadsUnavailabilityAsAbsence(t *testing.T) {
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}},
		func(*http.Request) *http.Response {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no durable store"))}
		}))

	_, err := store.InspectObject(context.Background(), hangar.KindCheckpoint, checkpointObjectTestRef(t, "e", 17).Digest)
	if err == nil {
		t.Fatal("expected an unavailable durable store to fail")
	}
	if errors.Is(err, hangar.ErrNotFound) {
		t.Fatalf("unavailability was reported as absence: %v", err)
	}
}

func TestCheckpointObjectStoreFallsBackToAnotherDaemonWhenOneIsUnreachable(t *testing.T) {
	var seen []string
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}, {NodeName: "node-b", Address: "10.0.0.2"}},
		func(request *http.Request) *http.Response {
			seen = append(seen, request.URL.Host)
			if request.URL.Host == "10.0.0.1:7780" {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("no durable store"))}
			}
			return &http.Response{StatusCode: http.StatusNoContent, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}
		}))

	if err := store.DeleteObject(context.Background(), checkpointObjectTestRef(t, "a", 11)); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if len(seen) != 2 || seen[1] != "10.0.0.2:7780" {
		t.Fatalf("expected a fallback to the second daemon, got %v", seen)
	}
}

// A definitive answer ends the attempt. Retrying a proven absence or a proven
// conflict on another node cannot change it and only risks a second node
// answering differently for the wrong reason.
func TestCheckpointObjectStoreDoesNotRetryADefinitiveAnswer(t *testing.T) {
	var attempts int
	store := NewCheckpointObjectStore(checkpointCaptureDaemonClient(
		[]DaemonEndpoint{{NodeName: "node-a", Address: "10.0.0.1"}, {NodeName: "node-b", Address: "10.0.0.2"}},
		func(*http.Request) *http.Response {
			attempts++
			return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("not found"))}
		}))

	if _, err := store.InspectObject(context.Background(), hangar.KindCheckpoint, checkpointObjectTestRef(t, "e", 17).Digest); !errors.Is(err, hangar.ErrNotFound) {
		t.Fatalf("InspectObject = %v, want hangar.ErrNotFound", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func decodeCheckpointObjectTestJSON(body []byte, destination any) error {
	return json.Unmarshal(body, destination)
}
