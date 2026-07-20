package dispatcher_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dispatcherapi "github.com/concourse/concourse/agent/api/dispatcher"
)

func newHandler(bootFlag bool) (*dispatcherapi.Handler, *dispatcherapi.MemoryStore) {
	store := dispatcherapi.NewMemoryStore()
	h := dispatcherapi.NewHandler(store, bootFlag, func(r *http.Request) string {
		return r.Header.Get("X-Test-User")
	})
	return h, store
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

func TestGetFallsBackToBootDefaultOff(t *testing.T) {
	h, _ := newHandler(false)
	resp := getStatus(t, h)
	if resp.Mode != "off" || resp.Source != "boot-default" || resp.BootDefault != "off" {
		t.Fatalf("got %+v, want mode=off source=boot-default boot_default=off", resp)
	}
	if resp.UpdatedAt != nil || resp.UpdatedBy != nil {
		t.Errorf("expected null updated_at/updated_by, got %+v", resp)
	}
}

func TestGetFallsBackToBootDefaultActive(t *testing.T) {
	h, _ := newHandler(true) // --agent-dispatcher-enabled
	resp := getStatus(t, h)
	if resp.Mode != "active" || resp.Source != "boot-default" || resp.BootDefault != "active" {
		t.Fatalf("got %+v, want mode=active source=boot-default boot_default=active", resp)
	}
}

func TestPutSetsModeAndReturnsNewState(t *testing.T) {
	h, _ := newHandler(false) // boot default off; runtime override to paused

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
	if resp.Mode != "paused" || resp.Source != "setting" || resp.BootDefault != "off" {
		t.Fatalf("PUT response %+v, want mode=paused source=setting boot_default=off", resp)
	}
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "tdm" {
		t.Errorf("updated_by = %v, want tdm", resp.UpdatedBy)
	}
	if resp.UpdatedAt == nil {
		t.Errorf("updated_at should be set, got nil")
	}

	// GET now reflects the persisted setting, overriding the boot default.
	after := getStatus(t, h)
	if after.Mode != "paused" || after.Source != "setting" {
		t.Fatalf("GET after PUT = %+v, want mode=paused source=setting", after)
	}
}

func TestPutMissingUserDefaultsToAdmin(t *testing.T) {
	h, _ := newHandler(false)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/agent/dispatcher", strings.NewReader(`{"mode":"active"}`))
	// no X-Test-User header -> identity ""
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d", rec.Code)
	}
	resp := getStatus(t, h)
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "admin" {
		t.Errorf("updated_by = %v, want admin fallback", resp.UpdatedBy)
	}
}

func TestPutRejectsInvalidMode(t *testing.T) {
	h, _ := newHandler(false)
	for _, bad := range []string{`{"mode":"on"}`, `{"mode":""}`, `{}`, `not json`} {
		rec := httptest.NewRecorder()
		h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/dispatcher", strings.NewReader(bad)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %q = %d, want 400", bad, rec.Code)
		}
	}
	// Nothing persisted -> still boot-default.
	if resp := getStatus(t, h); resp.Source != "boot-default" {
		t.Errorf("invalid PUTs must not persist; source = %s", resp.Source)
	}
}
