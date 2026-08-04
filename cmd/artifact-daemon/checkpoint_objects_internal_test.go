package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
)

func newCheckpointObjectServer(t *testing.T) (*Server, *hangarfakes.FakeStore) {
	t.Helper()
	server := NewServer(lagertest.NewTestLogger("checkpoint-objects"), t.TempDir(), "node")
	durable := new(hangarfakes.FakeStore)
	server.SetHangarStore(durable)
	return server, durable
}

func checkpointObjectDigest(content string) hangar.Digest {
	sum := sha256.Sum256([]byte(content))
	return hangar.Digest("sha256:" + hex.EncodeToString(sum[:]))
}

func checkpointObjectRef(t *testing.T, digest hangar.Digest, generation int64) hangar.ObjectRef {
	t.Helper()
	ref, err := hangar.NewObjectRef(hangar.KindCheckpoint, digest, generation)
	if err != nil {
		t.Fatalf("checkpoint object ref: %v", err)
	}
	return ref
}

func checkpointObjectBody(t *testing.T, value any) *strings.Reader {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	return strings.NewReader(string(encoded))
}

func TestCheckpointObjectInspectAnswersWithTheStoredGeneration(t *testing.T) {
	server, durable := newCheckpointObjectServer(t)
	digest := checkpointObjectDigest("landed checkpoint")
	durable.InspectReturns(hangar.Attributes{Ref: checkpointObjectRef(t, digest, 19)}, nil)

	response := serveSnapshotRequest(server, http.MethodPost, "/checkpoints/v1/objects/inspect",
		checkpointObjectBody(t, map[string]string{"digest": string(digest)}))
	if response.Code != http.StatusOK {
		t.Fatalf("inspect = %d, want 200: %s", response.Code, response.Body.String())
	}

	calls := durable.InspectCalls()
	if len(calls) != 1 {
		t.Fatalf("durable inspect calls = %d, want 1", len(calls))
	}
	if calls[0].Kind != hangar.KindCheckpoint {
		t.Fatalf("inspected kind = %q, want %q", calls[0].Kind, hangar.KindCheckpoint)
	}
	if calls[0].Digest != digest {
		t.Fatalf("inspected digest = %q, want %q", calls[0].Digest, digest)
	}
	if calls[0].MaxUncompressedBytes != server.checkpointMaxBytes {
		t.Fatalf("inspect bound = %d, want the daemon ceiling %d", calls[0].MaxUncompressedBytes, server.checkpointMaxBytes)
	}

	var body hangar.ObjectRef
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode inspect response: %v", err)
	}
	if body != checkpointObjectRef(t, digest, 19) {
		t.Fatalf("inspect response = %+v", body)
	}
}

func TestCheckpointObjectInspectReportsAnAbsentObjectAsNotFound(t *testing.T) {
	server, durable := newCheckpointObjectServer(t)
	durable.InspectReturns(hangar.Attributes{}, hangar.ErrNotFound)

	response := serveSnapshotRequest(server, http.MethodPost, "/checkpoints/v1/objects/inspect",
		checkpointObjectBody(t, map[string]string{"digest": string(checkpointObjectDigest("never landed"))}))
	if response.Code != http.StatusNotFound {
		t.Fatalf("inspect = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestCheckpointObjectDeleteIsPinnedToTheRequestedGeneration(t *testing.T) {
	server, durable := newCheckpointObjectServer(t)
	ref := checkpointObjectRef(t, checkpointObjectDigest("reclaimable checkpoint"), 23)

	response := serveSnapshotRequest(server, http.MethodPost, "/checkpoints/v1/objects/delete", checkpointObjectBody(t, ref))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", response.Code, response.Body.String())
	}

	calls := durable.DeleteCalls()
	if len(calls) != 1 {
		t.Fatalf("durable delete calls = %d, want 1", len(calls))
	}
	if calls[0].Ref != ref {
		t.Fatalf("deleted ref = %+v, want %+v", calls[0].Ref, ref)
	}
}

func TestCheckpointObjectDeleteRefusesAnythingButACanonicalCheckpointReference(t *testing.T) {
	digest := checkpointObjectDigest("reclaimable checkpoint")
	canonical := checkpointObjectRef(t, digest, 23)
	snapshotKey, err := hangar.Key(hangar.KindSnapshot, digest)
	if err != nil {
		t.Fatalf("snapshot key: %v", err)
	}
	for name, ref := range map[string]hangar.ObjectRef{
		"snapshot kind": {Kind: hangar.KindSnapshot, Digest: digest, Key: snapshotKey, Generation: 23},
		"foreign key":   {Kind: hangar.KindCheckpoint, Digest: digest, Key: snapshotKey, Generation: 23},
		"no generation": {Kind: hangar.KindCheckpoint, Digest: digest, Key: canonical.Key, Generation: 0},
		"malformed digest": {
			Kind: hangar.KindCheckpoint, Digest: "sha256:not-a-digest", Key: canonical.Key, Generation: 23,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, durable := newCheckpointObjectServer(t)

			response := serveSnapshotRequest(server, http.MethodPost, "/checkpoints/v1/objects/delete", checkpointObjectBody(t, ref))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("delete = %d, want 400: %s", response.Code, response.Body.String())
			}
			if calls := durable.DeleteCalls(); len(calls) != 0 {
				t.Fatalf("a rejected reference reached the durable store: %+v", calls)
			}
		})
	}
}

func TestCheckpointObjectDeleteReportsAGenerationConflictDistinctly(t *testing.T) {
	server, durable := newCheckpointObjectServer(t)
	durable.DeleteReturns(fmt.Errorf("%w: delete generation no longer matches", hangar.ErrConflict))
	ref := checkpointObjectRef(t, checkpointObjectDigest("re-uploaded checkpoint"), 23)

	response := serveSnapshotRequest(server, http.MethodPost, "/checkpoints/v1/objects/delete", checkpointObjectBody(t, ref))
	if response.Code != http.StatusConflict {
		t.Fatalf("delete = %d, want 409: %s", response.Code, response.Body.String())
	}
}

// A daemon with no durable store knows nothing about any object. Answering 404
// there would be indistinguishable from a proven absence, and a caller that
// believed it would forget the row that is the only remaining name for content
// that may well exist. Unavailability gets its own status.
func TestCheckpointObjectRoutesDistinguishNoDurableStoreFromAbsence(t *testing.T) {
	digest := checkpointObjectDigest("no durable store")
	for name, request := range map[string]struct {
		route string
		body  any
	}{
		"inspect": {route: "/checkpoints/v1/objects/inspect", body: map[string]string{"digest": string(digest)}},
		"delete":  {route: "/checkpoints/v1/objects/delete", body: checkpointObjectRef(t, digest, 23)},
	} {
		t.Run(name, func(t *testing.T) {
			server := NewServer(lagertest.NewTestLogger("checkpoint-objects"), t.TempDir(), "node")

			response := serveSnapshotRequest(server, http.MethodPost, request.route, checkpointObjectBody(t, request.body))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s = %d, want 503: %s", request.route, response.Code, response.Body.String())
			}
		})
	}
}
