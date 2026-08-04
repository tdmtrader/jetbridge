package jetbridge

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

const inventoryDigest = snapshot.Digest("sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111")

func TestListDurableObjectsParsesInventory(t *testing.T) {
	body := `{"objects":[{"digest":"` + string(inventoryDigest) + `",` +
		`"key":"hangar/v1/snapshots/sha256/x.tar.zst","generation":42,"bytes":4096,` +
		`"created_at":"2026-08-01T12:00:00Z"}]}`
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/snapshots/v1/durable-objects" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.Path)
		}
		return response(http.StatusOK, []byte(body)), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	var listed []snapshot.DurableObject
	if err := store.ListDurableObjects(context.Background(), func(object snapshot.DurableObject) error {
		listed = append(listed, object)
		return nil
	}); err != nil {
		t.Fatalf("ListDurableObjects: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed = %d, want 1", len(listed))
	}
	object := listed[0]
	if object.Digest != inventoryDigest || object.Generation != 42 || object.Bytes != 4096 {
		t.Fatalf("object = %+v", object)
	}
	if !object.CreatedAt.Equal(time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("CreatedAt = %v", object.CreatedAt)
	}
	if err := object.Validate(); err != nil {
		t.Fatalf("parsed object is not usable by the sweep: %v", err)
	}
}

// An object whose creation time cannot be parsed must reach the sweep with a
// zero time, which the sweep refuses. Substituting "now" would hide it;
// substituting a past time would make it instantly deletable.
func TestListDurableObjectsLeavesUnparsableTimestampsZero(t *testing.T) {
	body := `{"objects":[{"digest":"` + string(inventoryDigest) + `",` +
		`"key":"hangar/v1/snapshots/sha256/x.tar.zst","generation":42,"bytes":4096,` +
		`"created_at":"whenever"}]}`
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, []byte(body)), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	var listed []snapshot.DurableObject
	if err := store.ListDurableObjects(context.Background(), func(object snapshot.DurableObject) error {
		listed = append(listed, object)
		return nil
	}); err != nil {
		t.Fatalf("ListDurableObjects: %v", err)
	}
	if len(listed) != 1 || !listed[0].CreatedAt.IsZero() {
		t.Fatalf("listed = %+v, want one object with a zero creation time", listed)
	}
	if err := listed[0].Validate(); err == nil {
		t.Fatal("an unaged object must not validate for deletion")
	}
}

// The failure body is deliberately well-formed JSON that would decode into an
// empty inventory. Only the status check can reject it, so this cannot pass by
// accidentally failing to parse an empty body — a partial or degraded listing
// must never be mistaken for "the store holds nothing", which would make every
// referenced object look reclaimable at once.
func TestListDurableObjectsReportsDaemonFailure(t *testing.T) {
	for name, status := range map[string]int{
		"bad gateway":  http.StatusBadGateway,
		"not found":    http.StatusNotFound,
		"unauthorized": http.StatusUnauthorized,
		"server error": http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(status, []byte(`{"objects":[]}`)), nil
			}))
			store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

			visited := 0
			err := store.ListDurableObjects(context.Background(), func(snapshot.DurableObject) error {
				visited++
				return nil
			})
			if err == nil {
				t.Fatal("ListDurableObjects hid a daemon failure")
			}
			if visited != 0 {
				t.Fatalf("failed listing still yielded %d objects", visited)
			}
		})
	}
}

func TestDeleteDurableObjectSendsPinnedGeneration(t *testing.T) {
	var target string
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", request.Method)
		}
		target = request.URL.Path + "?" + request.URL.RawQuery
		return response(http.StatusNoContent, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	object := snapshot.DurableObject{
		Digest: inventoryDigest, Key: "hangar/v1/snapshots/sha256/x.tar.zst",
		Generation: 42, Bytes: 4096, CreatedAt: time.Now().UTC(),
	}
	if err := store.DeleteDurableObject(context.Background(), object); err != nil {
		t.Fatalf("DeleteDurableObject: %v", err)
	}
	if !strings.Contains(target, "/snapshots/v1/durable-objects/"+string(inventoryDigest)) {
		t.Fatalf("target = %q", target)
	}
	if !strings.Contains(target, "generation=42") {
		t.Fatalf("target = %q, want the judged generation pinned", target)
	}
}

// A conflict means the digest was rewritten after it was judged. That must
// surface as an error, never as a successful reclamation.
func TestDeleteDurableObjectSurfacesGenerationConflict(t *testing.T) {
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusConflict, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	object := snapshot.DurableObject{
		Digest: inventoryDigest, Key: "hangar/v1/snapshots/sha256/x.tar.zst",
		Generation: 42, Bytes: 4096, CreatedAt: time.Now().UTC(),
	}
	if err := store.DeleteDurableObject(context.Background(), object); err == nil {
		t.Fatal("DeleteDurableObject treated a generation conflict as success")
	}
}

func TestDeleteDurableObjectRejectsUnusableObject(t *testing.T) {
	var requests int
	client := snapshotDaemonClient(t, []string{"node-a"}, roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return response(http.StatusNoContent, nil), nil
	}))
	store, _ := NewSnapshotContentStore(client, &locationResolverStub{}, 1, testSnapshotArchiveLimits)

	if err := store.DeleteDurableObject(context.Background(), snapshot.DurableObject{
		Digest: inventoryDigest, Key: "k", Generation: 0, CreatedAt: time.Now().UTC(),
	}); err == nil {
		t.Fatal("DeleteDurableObject accepted an unpinned object")
	}
	if requests != 0 {
		t.Fatalf("rejected object still reached the daemon (%d requests)", requests)
	}
}
