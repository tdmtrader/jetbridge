package main_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/agent/hangar"
	"github.com/concourse/concourse/agent/hangar/hangarfakes"
	daemon "github.com/concourse/concourse/cmd/artifact-daemon"
)

const inventoryDigestA = hangar.Digest("sha256:" +
	"1111111111111111111111111111111111111111111111111111111111111111")

func inventoryServer(t *testing.T, durable *hangarfakes.FakeStore) *httptest.Server {
	t.Helper()
	server := daemon.NewServer(lagertest.NewTestLogger("durable-inventory"), t.TempDir(), "node-a")
	if durable != nil {
		server.SetHangarStore(durable)
	}
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestDurableInventoryReportsStoredObjects(t *testing.T) {
	ref, err := hangar.NewObjectRef(hangar.KindSnapshot, inventoryDigestA, 42)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	durable := &hangarfakes.FakeStore{}
	durable.ListReturns([]hangar.Attributes{{
		Ref: ref, CompressedBytes: 4096, UncompressedBytes: 8192, CreatedAt: created,
	}}, nil)

	ts := inventoryServer(t, durable)
	response, err := ts.Client().Get(ts.URL + "/snapshots/v1/durable-objects")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	var inventory struct {
		Objects []struct {
			Digest     string `json:"digest"`
			Key        string `json:"key"`
			Generation int64  `json:"generation"`
			Bytes      int64  `json:"bytes"`
			CreatedAt  string `json:"created_at"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(response.Body).Decode(&inventory); err != nil {
		t.Fatal(err)
	}
	if len(inventory.Objects) != 1 {
		t.Fatalf("objects = %d, want 1", len(inventory.Objects))
	}
	object := inventory.Objects[0]
	if object.Digest != string(inventoryDigestA) || object.Generation != 42 || object.Bytes != 4096 {
		t.Fatalf("object = %+v", object)
	}
	// The creation time is what the sweep's age threshold is evaluated against,
	// so it has to survive the wire exactly.
	parsed, err := time.Parse(time.RFC3339Nano, object.CreatedAt)
	if err != nil || !parsed.Equal(created) {
		t.Fatalf("created_at = %q (%v), want %v", object.CreatedAt, err, created)
	}
	if calls := durable.ListCalls(); len(calls) != 1 || calls[0].Kind != hangar.KindSnapshot {
		t.Fatalf("list calls = %+v, want one snapshot listing", calls)
	}
}

func TestDurableInventoryRequiresConfiguredStorage(t *testing.T) {
	ts := inventoryServer(t, nil)
	response, err := ts.Client().Get(ts.URL + "/snapshots/v1/durable-objects")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.StatusCode)
	}
}

func deleteDurable(t *testing.T, ts *httptest.Server, digest, query string) *http.Response {
	t.Helper()
	target := ts.URL + "/snapshots/v1/durable-objects/" + digest
	if query != "" {
		target += "?" + query
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := ts.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
	})
	return response
}

func TestDurableInventoryDeletePinsTheJudgedGeneration(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	ts := inventoryServer(t, durable)

	response := deleteDurable(t, ts, string(inventoryDigestA), "generation=42")
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", response.StatusCode)
	}
	calls := durable.DeleteCalls()
	if len(calls) != 1 {
		t.Fatalf("delete calls = %d, want 1", len(calls))
	}
	if calls[0].Ref.Generation != 42 || calls[0].Ref.Digest != inventoryDigestA {
		t.Fatalf("delete ref = %+v", calls[0].Ref)
	}
	if calls[0].Ref.Kind != hangar.KindSnapshot {
		t.Fatalf("delete kind = %s, want snapshots", calls[0].Ref.Kind)
	}
}

// Without a generation the delete would apply to whatever occupies the digest
// when the request lands, which is exactly the race the pin exists to stop.
func TestDurableInventoryDeleteRejectsUnpinnedAndMalformedRequests(t *testing.T) {
	for name, testCase := range map[string]struct{ digest, query string }{
		"missing generation":  {string(inventoryDigestA), ""},
		"empty generation":    {string(inventoryDigestA), "generation="},
		"zero generation":     {string(inventoryDigestA), "generation=0"},
		"negative generation": {string(inventoryDigestA), "generation=-1"},
		"nonnumeric":          {string(inventoryDigestA), "generation=latest"},
		"malformed digest":    {"not-a-digest", "generation=42"},
		"uppercase digest":    {strings.ToUpper(string(inventoryDigestA)), "generation=42"},
	} {
		t.Run(name, func(t *testing.T) {
			durable := &hangarfakes.FakeStore{}
			ts := inventoryServer(t, durable)
			response := deleteDurable(t, ts, testCase.digest, testCase.query)
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
			if calls := durable.DeleteCalls(); len(calls) != 0 {
				t.Fatalf("rejected request still deleted: %+v", calls)
			}
		})
	}
}

// A digest rewritten after it was judged must survive: the pinned delete fails
// rather than destroying the newer object.
func TestDurableInventoryDeleteReportsGenerationConflict(t *testing.T) {
	durable := &hangarfakes.FakeStore{}
	durable.DeleteReturns(hangar.ErrConflict)
	ts := inventoryServer(t, durable)

	response := deleteDurable(t, ts, string(inventoryDigestA), "generation=42")
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.StatusCode)
	}
}
