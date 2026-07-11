package credentials_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/credentials"
)

func newCredHandler() (*credentials.Handler, *credentials.MemoryBackend) {
	backend := credentials.NewMemoryBackend()
	backend.AddUser("sub-alice", 7, "alice")
	backend.AddUser(credentials.PlatformUserSub, 99, "platform")
	claims := func(r *http.Request) (string, string, bool, bool) {
		sub := r.Header.Get("X-Test-Sub")
		isAdmin := r.Header.Get("X-Test-Admin") == "true"
		return sub, "alice", isAdmin, sub != ""
	}
	return credentials.NewHandler(backend, claims), backend
}

func TestSetStoresCredentialForSelf(t *testing.T) {
	h, backend := newCredHandler()
	exp := time.Now().Add(365 * 24 * time.Hour).Unix()
	body := `{"kind":"anthropic_oauth","token":"sk-tok","expires_at":` + jsonInt(exp) + `,"jira_account_id":"acct-1"}`
	req := httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
	req.Header.Set("X-Test-Sub", "sub-alice")
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	cred, found, _ := backend.Resolve(7, "anthropic_oauth")
	if !found || cred.Token != "sk-tok" || cred.ExpiresAt != exp {
		t.Fatalf("stored: %+v found=%v", cred, found)
	}
	status, _ := backend.Status(7)
	if status[0].JiraAccountID != "acct-1" {
		t.Fatalf("jira seam not stored: %+v", status[0])
	}
}

func TestSetRejectsUnknownUserAndBadBodies(t *testing.T) {
	h, _ := newCredHandler()

	req := httptest.NewRequest("PUT", "/api/v1/agent/user-credentials",
		strings.NewReader(`{"kind":"anthropic_oauth","token":"t"}`))
	rec := httptest.NewRecorder()
	h.Set(rec, req) // no claims header -> unauthenticated
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no claims: got %d", rec.Code)
	}

	req = httptest.NewRequest("PUT", "/api/v1/agent/user-credentials",
		strings.NewReader(`{"kind":"anthropic_oauth","token":"t"}`))
	req.Header.Set("X-Test-Sub", "sub-never-logged-in")
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user: got %d", rec.Code)
	}

	for _, body := range []string{`{"kind":"openai","token":"t"}`, `{"kind":"anthropic_oauth","token":""}`, `nope`} {
		req = httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
		req.Header.Set("X-Test-Sub", "sub-alice")
		rec = httptest.NewRecorder()
		h.Set(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: got %d", body, rec.Code)
		}
	}
}

func TestStatusReturnsOnlySelfWithoutTokens(t *testing.T) {
	h, backend := newCredHandler()
	backend.AddUser("sub-bob", 8, "bob")
	_ = backend.Put(7, "alice", "anthropic_oauth", "sk-a", time.Now().Add(time.Hour))
	_ = backend.Put(8, "bob", "anthropic_oauth", "sk-b", time.Time{})

	req := httptest.NewRequest("GET", "/api/v1/agent/user-credentials", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	rec := httptest.NewRecorder()
	h.Status(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "sk-a") || strings.Contains(rec.Body.String(), "sk-b") {
		t.Fatalf("token leaked: %s", rec.Body.String())
	}
	var creds []credentials.Credential
	if err := json.Unmarshal(rec.Body.Bytes(), &creds); err != nil {
		t.Fatal(err)
	}
	if len(creds) != 1 || creds[0].UserID != 7 {
		t.Fatalf("got %+v", creds)
	}
}

func TestPlatformCredentialIsAdminOnly(t *testing.T) {
	h, backend := newCredHandler()
	body := `{"kind":"anthropic_oauth","token":"sk-platform","user":"platform"}`

	req := httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
	req.Header.Set("X-Test-Sub", "sub-alice") // authenticated, NOT admin
	rec := httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin platform write: got %d", rec.Code)
	}

	req = httptest.NewRequest("PUT", "/api/v1/agent/user-credentials", strings.NewReader(body))
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Header.Set("X-Test-Admin", "true")
	rec = httptest.NewRecorder()
	h.Set(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin platform write: got %d: %s", rec.Code, rec.Body.String())
	}

	// The credential lands on the §1.13 service user's row, not the admin's.
	cred, found, _ := backend.Resolve(99, "anthropic_oauth")
	if !found || cred.Token != "sk-platform" {
		t.Fatalf("platform credential row: %+v found=%v", cred, found)
	}
	if _, found, _ := backend.Resolve(7, "anthropic_oauth"); found {
		t.Fatal("platform write leaked onto the admin's own row")
	}

	// Admin delete via ?user=platform.
	req = httptest.NewRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth?user=platform", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Header.Set("X-Test-Admin", "true")
	req.Form = map[string][]string{":kind": {"anthropic_oauth"}}
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("admin platform delete: got %d", rec.Code)
	}
	if _, found, _ := backend.Resolve(99, "anthropic_oauth"); found {
		t.Fatal("platform credential survived delete")
	}
}

func TestDeleteByKind(t *testing.T) {
	h, backend := newCredHandler()
	_ = backend.Put(7, "alice", "anthropic_oauth", "sk-a", time.Time{})

	req := httptest.NewRequest("DELETE", "/api/v1/agent/user-credentials/anthropic_oauth", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Form = map[string][]string{":kind": {"anthropic_oauth"}}
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("got %d", rec.Code)
	}
	if _, found, _ := backend.Resolve(7, "anthropic_oauth"); found {
		t.Fatal("credential survived delete")
	}

	req = httptest.NewRequest("DELETE", "/api/v1/agent/user-credentials/openai", nil)
	req.Header.Set("X-Test-Sub", "sub-alice")
	req.Form = map[string][]string{":kind": {"openai"}}
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad kind: got %d", rec.Code)
	}
}

func jsonInt(i int64) string {
	b, _ := json.Marshal(i)
	return string(b)
}
