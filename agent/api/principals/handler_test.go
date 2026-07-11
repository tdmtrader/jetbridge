package principals_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/api/principals"
)

func newHandler() (*principals.Handler, *principals.MemoryStore) {
	store := principals.NewMemoryStore()
	return principals.NewHandler(store, func(r *http.Request) string { return "test-admin" }), store
}

func TestCreatePrincipal(t *testing.T) {
	h, _ := newHandler()

	req := httptest.NewRequest("POST", "/api/v1/agent/principals",
		strings.NewReader(`{"name": "ci-agent-review", "description": "d", "scopes": ["reviews:write"]}`))
	rec := httptest.NewRecorder()
	h.CreatePrincipal(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body %s, want 201", rec.Code, rec.Body)
	}
	var resp struct {
		ID          int    `json:"id"`
		Name        string `json:"name"`
		Token       string `json:"token"`
		TokenPrefix string `json:"token_prefix"`
		CreatedBy   string `json:"created_by"`
		TeamName    string `json:"team_name"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(resp.Token, "cap1.") {
		t.Errorf("token = %q", resp.Token)
	}
	if resp.CreatedBy != "test-admin" {
		t.Errorf("created_by = %q, want server-derived test-admin", resp.CreatedBy)
	}
	if resp.TeamName != "main" {
		t.Errorf("team_name = %q, want default main", resp.TeamName)
	}
}

func TestCreatePrincipalRejectsBadSpecs(t *testing.T) {
	h, _ := newHandler()
	for name, body := range map[string]string{
		"bad json":      `{`,
		"missing name":  `{"scopes": ["reviews:write"]}`,
		"no scopes":     `{"name": "x"}`,
		"unknown scope": `{"name": "x", "scopes": ["reviews:read"]}`,
	} {
		req := httptest.NewRequest("POST", "/api/v1/agent/principals", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.CreatePrincipal(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d, want 400", name, rec.Code)
		}
	}
}

func TestListPrincipalsOmitsTokens(t *testing.T) {
	h, store := newHandler()
	_, token, err := store.Create(principals.CreateSpec{Name: "g", Scopes: []string{principals.ScopeCostsWrite}})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/v1/agent/principals", nil)
	rec := httptest.NewRecorder()
	h.ListPrincipals(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), token) || strings.Contains(rec.Body.String(), principals.HashToken(token)) {
		t.Error("list response leaked token material")
	}
}

func TestRevokePrincipal(t *testing.T) {
	h, store := newHandler()
	created, _, err := store.Create(principals.CreateSpec{Name: "g", Scopes: []string{principals.ScopeCostsWrite}})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("DELETE", "/api/v1/agent/principals/"+strconv.Itoa(created.ID), nil)
	req.Form = url.Values{":principal_id": {strconv.Itoa(created.ID)}}
	rec := httptest.NewRecorder()
	h.RevokePrincipal(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", rec.Code)
	}

	req = httptest.NewRequest("DELETE", "/api/v1/agent/principals/999", nil)
	req.Form = url.Values{":principal_id": {"999"}}
	rec = httptest.NewRecorder()
	h.RevokePrincipal(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id: code = %d, want 404", rec.Code)
	}
}
