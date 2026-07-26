package actions_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	actionsapi "github.com/concourse/concourse/agent/api/actions"
)

func newHandler() (*actionsapi.Handler, *actionsapi.MemoryStore) {
	store := actionsapi.NewMemoryStore()
	return actionsapi.NewHandler(store, func(r *http.Request) string {
		return r.Header.Get("X-Test-User")
	}), store
}

func getStatus(t *testing.T, h *actionsapi.Handler) actionsapi.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest("GET", "/api/v1/agent/actions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp actionsapi.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp
}

func TestGetReportsActiveUntilTheSwitchIsEngaged(t *testing.T) {
	h, _ := newHandler()
	resp := getStatus(t, h)
	if resp.Mode != "active" || resp.Source != "default" {
		t.Fatalf("got %+v, want mode=active source=default", resp)
	}
	if resp.UpdatedAt != nil || resp.UpdatedBy != nil {
		t.Errorf("expected null provenance before the switch is set, got %+v", resp)
	}
}

func TestPutSuppressesAndRecordsTheActor(t *testing.T) {
	h, _ := newHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{"mode":"suppressed"}`))
	req.Header.Set("X-Test-User", "tdm")
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body %s", rec.Code, rec.Body.String())
	}

	resp := getStatus(t, h)
	if resp.Mode != "suppressed" || resp.Source != "setting" {
		t.Fatalf("got %+v, want mode=suppressed source=setting", resp)
	}
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "tdm" || resp.UpdatedAt == nil {
		t.Fatalf("provenance = %+v, want updated_by=tdm and a timestamp", resp)
	}
}

func TestPutRejectsAnUnknownMode(t *testing.T) {
	h, _ := newHandler()
	rec := httptest.NewRecorder()
	h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{"mode":"halt"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", rec.Code)
	}
	if resp := getStatus(t, h); resp.Mode != "active" {
		t.Fatalf("rejected PUT changed the mode to %q", resp.Mode)
	}
}

func TestPutRejectsInvalidJSON(t *testing.T) {
	h, _ := newHandler()
	rec := httptest.NewRecorder()
	h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT status = %d, want 400", rec.Code)
	}
}

func TestPutWithoutAnIdentityRecordsAnHonestSentinel(t *testing.T) {
	h, _ := newHandler()
	rec := httptest.NewRecorder()
	h.Set(rec, httptest.NewRequest("PUT", "/api/v1/agent/actions", strings.NewReader(`{"mode":"suppressed"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d", rec.Code)
	}
	resp := getStatus(t, h)
	if resp.UpdatedBy == nil || *resp.UpdatedBy != "unknown" {
		t.Fatalf("updated_by = %+v, want the \"unknown\" sentinel", resp.UpdatedBy)
	}
}

type failingStore struct{}

func (failingStore) GetActionsSetting() (string, time.Time, string, bool, error) {
	return "", time.Time{}, "", false, errors.New("connection refused")
}
func (failingStore) SetActionsMode(string, string) error { return errors.New("connection refused") }

// A read fault must be an ERROR on the wire, never a cheerful "active": an
// operator checking whether the brake is on must not be told it is off because
// the database is down.
func TestGetSurfacesAReadFaultInsteadOfGuessing(t *testing.T) {
	h := actionsapi.NewHandler(failingStore{}, func(*http.Request) string { return "tdm" })
	rec := httptest.NewRecorder()
	h.Get(rec, httptest.NewRequest("GET", "/api/v1/agent/actions", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "active") {
		t.Fatalf("read fault reported a mode: %s", rec.Body.String())
	}
}
