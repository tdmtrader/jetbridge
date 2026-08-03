package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
)

func repairMetadataTarget(content []byte) (string, string) {
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	return "/snapshots/v1/repair-durable-metadata/" + digest, digest
}

func newRepairMetadataServer(t *testing.T) (*Server, *hangarfakes.FakeStore) {
	t.Helper()
	server := NewServer(lagertest.NewTestLogger("snapshot-metadata-repair"), t.TempDir(), "node")
	durable := new(hangarfakes.FakeStore)
	server.SetHangarStore(durable)
	return server, durable
}

func TestRepairDurableMetadataAsksTheDurableStoreForTheRequestedSnapshot(t *testing.T) {
	server, durable := newRepairMetadataServer(t)
	target, digest := repairMetadataTarget([]byte("durable snapshot"))
	ref, err := hangar.NewObjectRef(hangar.KindSnapshot, hangar.Digest("sha256:"+digest), 7)
	if err != nil {
		t.Fatal(err)
	}
	durable.RepairDerivableMetadataReturns(hangar.Attributes{Ref: ref, UncompressedBytes: 4096}, nil)

	response := serveSnapshotRequest(server, http.MethodPost, target, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("repair = %d, want 200: %s", response.Code, response.Body.String())
	}

	calls := durable.RepairDerivableMetadataCalls()
	if len(calls) != 1 {
		t.Fatalf("durable repair calls = %d, want 1", len(calls))
	}
	if calls[0].Kind != hangar.KindSnapshot {
		t.Fatalf("repaired kind = %q, want %q", calls[0].Kind, hangar.KindSnapshot)
	}
	if calls[0].Digest != hangar.Digest("sha256:"+digest) {
		t.Fatalf("repaired digest = %q, want sha256:%s", calls[0].Digest, digest)
	}
	if calls[0].MaxUncompressedBytes != server.snapshotMaxBytes {
		t.Fatalf("repair limit = %d, want the daemon ceiling %d", calls[0].MaxUncompressedBytes, server.snapshotMaxBytes)
	}

	var body struct {
		Digest            string `json:"digest"`
		UncompressedBytes int64  `json:"uncompressed_bytes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode repair response: %v", err)
	}
	if body.Digest != "sha256:"+digest || body.UncompressedBytes != 4096 {
		t.Fatalf("repair response = %+v", body)
	}
}

func TestRepairDurableMetadataReportsUnprovableContentAsTerminal(t *testing.T) {
	for name, cause := range map[string]error{
		"content does not prove the key digest": fmt.Errorf("%w: object digest does not match key digest", hangar.ErrCorrupt),
		"metadata is not derivable":             fmt.Errorf("%w: unknown metadata key", hangar.ErrUnrepairable),
	} {
		t.Run(name, func(t *testing.T) {
			server, durable := newRepairMetadataServer(t)
			target, _ := repairMetadataTarget([]byte("unprovable snapshot"))
			durable.RepairDerivableMetadataReturns(hangar.Attributes{}, cause)

			response := serveSnapshotRequest(server, http.MethodPost, target, nil)
			if response.Code != http.StatusUnprocessableEntity {
				t.Fatalf("repair = %d, want 422", response.Code)
			}
		})
	}
}

func TestRepairDurableMetadataMapsStoreOutcomesToDistinctStatuses(t *testing.T) {
	for name, expectation := range map[string]struct {
		err  error
		code int
	}{
		"missing object":    {err: hangar.ErrNotFound, code: http.StatusNotFound},
		"concurrent change": {err: hangar.ErrConflict, code: http.StatusConflict},
		"transport failure": {err: fmt.Errorf("dial durable store: connection refused"), code: http.StatusBadGateway},
	} {
		t.Run(name, func(t *testing.T) {
			server, durable := newRepairMetadataServer(t)
			target, _ := repairMetadataTarget([]byte("mapped outcome"))
			durable.RepairDerivableMetadataReturns(hangar.Attributes{}, expectation.err)

			response := serveSnapshotRequest(server, http.MethodPost, target, nil)
			if response.Code != expectation.code {
				t.Fatalf("repair = %d, want %d", response.Code, expectation.code)
			}
		})
	}
}

func TestRepairDurableMetadataRejectsMalformedDigestsBeforeTouchingTheStore(t *testing.T) {
	for _, digest := range []string{
		"not-a-digest",
		strings.Repeat("A", 64),
		strings.Repeat("a", 63),
		"sha256:" + strings.Repeat("a", 64),
	} {
		t.Run(digest, func(t *testing.T) {
			server, durable := newRepairMetadataServer(t)
			response := serveSnapshotRequest(
				server,
				http.MethodPost,
				"/snapshots/v1/repair-durable-metadata/"+digest,
				nil,
			)
			if response.Code != http.StatusBadRequest && response.Code != http.StatusNotFound {
				t.Fatalf("repair of %q = %d, want a rejection", digest, response.Code)
			}
			if calls := durable.RepairDerivableMetadataCalls(); len(calls) != 0 {
				t.Fatalf("malformed digest reached the durable store: %+v", calls)
			}
		})
	}
}

func TestRepairDurableMetadataIsNotFoundWithoutADurableStore(t *testing.T) {
	server := NewServer(lagertest.NewTestLogger("no-durable-store"), t.TempDir(), "node")
	target, _ := repairMetadataTarget([]byte("no durable store"))

	response := serveSnapshotRequest(server, http.MethodPost, target, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("repair without a durable store = %d, want 404", response.Code)
	}
}

func TestRepairDurableMetadataRefusesConcurrentProvingRatherThanQueueingIt(t *testing.T) {
	server, durable := newRepairMetadataServer(t)
	target, _ := repairMetadataTarget([]byte("bounded proving"))

	admitted := make(chan struct{})
	release := make(chan struct{})
	durable.SetRepairDerivableMetadataStub(func(
		context.Context,
		hangar.Kind,
		hangar.Digest,
		int64,
	) (hangar.Attributes, error) {
		close(admitted)
		<-release
		return hangar.Attributes{}, nil
	})

	go func() {
		request := httptest.NewRequest(http.MethodPost, target, nil)
		server.Handler().ServeHTTP(httptest.NewRecorder(), request)
	}()
	<-admitted

	second := serveSnapshotRequest(server, http.MethodPost, target, nil)
	close(release)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent repair = %d, want 429", second.Code)
	}
	if calls := durable.RepairDerivableMetadataCalls(); len(calls) != 1 {
		t.Fatalf("durable repair calls = %d, want the second request refused before proving", len(calls))
	}
}

func TestRepairDurableMetadataRequiresPOST(t *testing.T) {
	server, durable := newRepairMetadataServer(t)
	target, _ := repairMetadataTarget([]byte("wrong method"))

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		response := serveSnapshotRequest(server, method, target, nil)
		if response.Code == http.StatusOK {
			t.Fatalf("%s repair = 200, want a rejection", method)
		}
	}
	if calls := durable.RepairDerivableMetadataCalls(); len(calls) != 0 {
		t.Fatalf("non-POST reached the durable store: %+v", calls)
	}
}
