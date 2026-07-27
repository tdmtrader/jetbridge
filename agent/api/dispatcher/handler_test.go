package dispatcher_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dispatcherapi "github.com/concourse/concourse/agent/api/dispatcher"
	"github.com/concourse/concourse/agent/api/dispatcher/dispatchertest"
)

func newHandler(store dispatcherapi.Store) *dispatcherapi.Handler {
	return dispatcherapi.NewHandler(store, func(r *http.Request) string {
		return r.Header.Get("X-Test-User")
	})
}

func getStatus(t *testing.T, h *dispatcherapi.Handler) dispatcherapi.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest("GET", "/api/v1/agent/dispatcher", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp dispatcherapi.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestGetReportsTheSeededModeWithProvenance(t *testing.T) {
	resp := getStatus(t, newHandler(dispatchertest.NewMemoryStore()))
	if resp.Mode != "off" {
		t.Fatalf("seeded mode = %q, want off", resp.Mode)
	}
	if resp.UpdatedAt == nil || resp.UpdatedBy == nil {
		t.Fatalf("a seeded row always has provenance, got %+v", resp)
	}
}

func TestGetWithoutARowFailsSafeToOff(t *testing.T) {
	// The row can only be absent if someone deleted it. No boot flag can
	// resurrect auto-dispatch behind that.
	resp := getStatus(t, newHandler(dispatchertest.NewMemoryStoreWithoutRow()))
	if resp.Mode != "off" {
		t.Fatalf("missing row must read as off, got %q", resp.Mode)
	}
	if resp.UpdatedAt != nil || resp.UpdatedBy != nil {
		t.Errorf("expected null updated_at/updated_by, got %+v", resp)
	}
}

func TestGetHasNoBootProvenanceOnTheWire(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(dispatchertest.NewMemoryStore()).
		Get(rec, httptest.NewRequest("GET", "/api/v1/agent/dispatcher", nil))
	var wire map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"source", "boot_default"} {
		if _, present := wire[retired]; present {
			t.Errorf("%q must not be on the wire: the setting is the only authority (%v)", retired, wire)
		}
	}
	if _, present := wire["mode"]; !present {
		t.Errorf("mode must be on the wire, got %v", wire)
	}
}

func TestPutSetsModeAndReturnsNewState(t *testing.T) {
	h := newHandler(dispatchertest.NewMemoryStore())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/agent/dispatcher", strings.NewReader(`{"mode":"paused"}`))
	req.Header.Set("X-Test-User", "tdm")
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp dispatcherapi.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mode != "paused" {
		t.Fatalf("PUT response %+v, want mode=paused", resp)
	}
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "tdm" {
		t.Errorf("updated_by = %v, want tdm", resp.UpdatedBy)
	}
	if resp.UpdatedAt == nil {
		t.Errorf("updated_at should be set, got nil")
	}

	if after := getStatus(t, h); after.Mode != "paused" {
		t.Fatalf("GET after PUT = %+v, want mode=paused", after)
	}
}

func TestPutMissingUserRecordsUnknown(t *testing.T) {
	h := newHandler(dispatchertest.NewMemoryStore())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/agent/dispatcher", strings.NewReader(`{"mode":"active"}`))
	// no X-Test-User header -> identity "" -> recorded as the honest "unknown"
	// sentinel, NOT fabricated as a real "admin" actor.
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rec.Code)
	}
	resp := getStatus(t, h)
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "unknown" {
		t.Errorf("updated_by = %v, want unknown sentinel", resp.UpdatedBy)
	}
}

func TestPutRejectsInvalidMode(t *testing.T) {
	h := newHandler(dispatchertest.NewMemoryStore())
	for _, bad := range []string{`{"mode":"on"}`, `{"mode":""}`, `{}`, `not json`} {
		rec := httptest.NewRecorder()
		h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/dispatcher", strings.NewReader(bad)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %q = %d, want 400", bad, rec.Code)
		}
	}
	// Nothing persisted -> still the seeded mode, still by the migration.
	resp := getStatus(t, h)
	if resp.Mode != "off" || resp.UpdatedBy == nil || *resp.UpdatedBy != "migration" {
		t.Errorf("invalid PUTs must not persist; got %+v", resp)
	}
}
