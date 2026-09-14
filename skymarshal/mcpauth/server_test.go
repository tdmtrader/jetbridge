package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// This transaction fake exercises the HTTP boundary and rollback semantics.
// SQL persistence/concurrency and real provider behavior are covered by Brine.
type memoryStore struct {
	mu      sync.Mutex
	records map[string][]byte
}
type memoryTx struct{ records map[string][]byte }

func (s *memoryStore) WithTx(_ context.Context, f func(Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := map[string][]byte{}
	for key, data := range s.records {
		records[key] = append([]byte(nil), data...)
	}
	if err := f(&memoryTx{records}); err != nil {
		return err
	}
	s.records = records
	return nil
}
func (*memoryTx) Lock(string, bool) error { return nil }
func (t *memoryTx) Get(kind, key string, dst any) error {
	data, ok := t.records[kind+":"+key]
	if !ok {
		return ErrNotFound
	}
	return json.Unmarshal(data, dst)
}
func (t *memoryTx) Put(kind, key string, value any, _ time.Time) error {
	data, err := json.Marshal(value)
	if err == nil {
		t.records[kind+":"+key] = data
	}
	return err
}
func (t *memoryTx) Delete(kind, key string) error { delete(t.records, kind+":"+key); return nil }
func (t *memoryTx) List(kind string) ([]Record, error) {
	var list []Record
	for key, data := range t.records {
		if strings.HasPrefix(key, kind+":") {
			list = append(list, Record{key[len(kind)+1:], data})
		}
	}
	return list, nil
}

type fakeProvider struct {
	token       string
	sequence    int
	groups      []any
	failure     error
	usedRefresh string
}

func (p *fakeProvider) AuthorizationURL(state, verifier string) string {
	return "https://identity.example/auth?state=" + url.QueryEscape(state)
}
func (p *fakeProvider) next() Identity {
	p.sequence++
	p.token = fmt.Sprintf("provider-refresh-%d", p.sequence)
	return Identity{Claims: map[string]any{"sub": "user-subject", "name": "Ada", "groups": p.groups, "federated_claims": map[string]any{"connector_id": "test", "user_id": "ada"}}, RefreshToken: p.token}
}
func (p *fakeProvider) Exchange(context.Context, string, string) (Identity, error) {
	return p.next(), nil
}
func (p *fakeProvider) Refresh(_ context.Context, token string) (Identity, error) {
	p.usedRefresh = token
	if p.failure != nil {
		return Identity{}, p.failure
	}
	if token != p.token {
		return Identity{}, ErrInvalidGrant
	}
	return p.next(), nil
}

type harness struct {
	server   *Server
	store    *memoryStore
	provider *fakeProvider
	now      time.Time
	cookies  map[string]*http.Cookie
	t        *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, store: &memoryStore{}, provider: &fakeProvider{groups: []any{"developers"}}, now: time.Date(2026, 9, 13, 0, 0, 0, 0, time.UTC), cookies: map[string]*http.Cookie{}}
	var err error
	h.server, err = NewServer(Config{Issuer: "https://jetbridge.example/mcp/oauth", Resource: "https://jetbridge.example/api/v1/mcp", Store: h.store, Provider: h.provider, Clients: []Client{{ID: "desktop", Name: "Desktop", RedirectURIs: []string{"http://127.0.0.1:7000/callback"}}}, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}
	return h
}
func (h *harness) request(method, path string, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	var body string
	if form != nil {
		body = form.Encode()
	}
	r := httptest.NewRequest(method, "https://jetbridge.example"+path, strings.NewReader(body))
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range h.cookies {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	h.server.ServeHTTP(w, r)
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(h.cookies, cookie.Name)
		} else {
			h.cookies[cookie.Name] = cookie
		}
	}
	return w
}
func requireStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
}
func hidden(t *testing.T, html, name string) string {
	t.Helper()
	matches := regexp.MustCompile(`name="` + regexp.QuoteMeta(name) + `" value="([^"]+)"`).FindStringSubmatch(html)
	if len(matches) != 2 {
		t.Fatalf("missing %s in %s", name, html)
	}
	return matches[1]
}
func (h *harness) consent(scopes []string) (string, string, string) {
	h.t.Helper()
	verifier := random()
	q := url.Values{"client_id": {"desktop"}, "redirect_uri": {"http://127.0.0.1:7000/callback"}, "response_type": {"code"}, "state": {"client-state"}, "resource": {h.server.config.Resource}, "scope": {strings.Join(scopes, " ")}, "code_challenge": {digest(verifier)}, "code_challenge_method": {"S256"}}
	w := h.request("GET", "/mcp/oauth/authorize?"+q.Encode(), nil)
	requireStatus(h.t, w, 303)
	providerURL, _ := url.Parse(w.Header().Get("Location"))
	state := providerURL.Query().Get("state")
	w = h.request("GET", "/mcp/oauth/callback?state="+url.QueryEscape(state)+"&code=provider-code", nil)
	requireStatus(h.t, w, 303)
	w = h.request("GET", w.Header().Get("Location"), nil)
	requireStatus(h.t, w, 200)
	return state, hidden(h.t, w.Body.String(), "csrf"), verifier
}
func (h *harness) code(scopes []string) (string, string) {
	h.t.Helper()
	state, csrf, verifier := h.consent(scopes)
	w := h.request("POST", "/mcp/oauth/consent", url.Values{"request": {state}, "csrf": {csrf}, "decision": {"allow"}, "scope": scopes})
	requireStatus(h.t, w, 303)
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("state") != "client-state" || u.Query().Get("iss") != h.server.config.Issuer {
		h.t.Fatal("callback lost client state or issuer")
	}
	return u.Query().Get("code"), verifier
}
func (h *harness) exchange(code, verifier string) *httptest.ResponseRecorder {
	return h.request("POST", "/mcp/oauth/token", url.Values{"client_id": {"desktop"}, "resource": {h.server.config.Resource}, "grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {"http://127.0.0.1:7000/callback"}})
}
func (h *harness) tokens(scopes ...string) tokenResponse {
	h.t.Helper()
	code, verifier := h.code(scopes)
	w := h.exchange(code, verifier)
	requireStatus(h.t, w, 200)
	var tokens tokenResponse
	if err := json.Unmarshal(w.Body.Bytes(), &tokens); err != nil {
		h.t.Fatal(err)
	}
	return tokens
}
func (h *harness) refresh(refresh, scope string) *httptest.ResponseRecorder {
	return h.request("POST", "/mcp/oauth/token", url.Values{"client_id": {"desktop"}, "resource": {h.server.config.Resource}, "grant_type": {"refresh_token"}, "refresh_token": {refresh}, "scope": {scope}})
}

func TestConsentRestrictsCapabilitiesAndPKCE(t *testing.T) {
	h := newHarness(t)
	state, csrf, verifier := h.consent([]string{ScopeRead, ScopePipelines, ScopeHijack})
	page := h.request("GET", "/mcp/oauth/consent?request="+state, nil).Body.String()
	if !strings.Contains(page, `value="read" checked`) || strings.Contains(page, `value="hijack" checked`) {
		t.Fatal("consent must default only read")
	}
	for _, form := range []url.Values{
		{"request": {state}, "csrf": {"wrong"}, "decision": {"allow"}, "scope": {ScopeRead}},
		{"request": {state}, "csrf": {csrf}, "decision": {"allow"}, "scope": {ScopeAdmin}},
	} {
		requireStatus(t, h.request("POST", "/mcp/oauth/consent", form), 400)
	}
	w := h.request("POST", "/mcp/oauth/consent", url.Values{"request": {state}, "csrf": {csrf}, "decision": {"allow"}, "scope": {ScopeRead}})
	requireStatus(t, w, 303)
	u, _ := url.Parse(w.Header().Get("Location"))
	code := u.Query().Get("code")
	requireStatus(t, h.exchange(code, random()), 400)
	w = h.exchange(code, verifier)
	requireStatus(t, w, 200)
	var tokens tokenResponse
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	p, err := h.server.Authenticate(context.Background(), tokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if !p.HasScope(ScopeRead) || p.HasScope(ScopePipelines) || p.HasScope(ScopeHijack) {
		t.Fatalf("incorrect scope grant: %v", p.Scopes)
	}
	requireStatus(t, h.exchange(code, verifier), 400)
	w = httptest.NewRecorder()
	if h.server.RequireScope(w, p, ScopeHijack) {
		t.Fatal("read grant allowed hijack")
	}
	requireStatus(t, w, 403)
	if !strings.Contains(w.Header().Get("WWW-Authenticate"), `scope="hijack"`) {
		t.Fatal("missing scope challenge")
	}
}

func TestRefreshRenewsIdentityWithoutIncreasingScope(t *testing.T) {
	h := newHarness(t)
	tokens := h.tokens(ScopeRead)
	h.now = h.now.Add(16 * time.Minute)
	if _, err := h.server.Authenticate(context.Background(), tokens.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("expired access accepted")
	}
	requireStatus(t, h.refresh(tokens.RefreshToken, ScopeHijack), 400)
	h.provider.groups = []any{"new-group"}
	w := h.refresh(tokens.RefreshToken, "")
	requireStatus(t, w, 200)
	var rotated tokenResponse
	_ = json.Unmarshal(w.Body.Bytes(), &rotated)
	p, err := h.server.Authenticate(context.Background(), rotated.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(p.Claims["groups"]) != "[new-group]" || p.HasScope(ScopeHijack) {
		t.Fatalf("stale or expanded principal: %#v", p)
	}
	if tokens.RefreshToken == rotated.RefreshToken {
		t.Fatal("refresh did not rotate")
	}
	requireStatus(t, h.refresh(tokens.RefreshToken, ""), 400)
	if _, err := h.server.Authenticate(context.Background(), rotated.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("refresh replay did not revoke grant")
	}
}

func TestIndependentConsentsShareCurrentIdentityButNotRevocation(t *testing.T) {
	h := newHarness(t)
	first := h.tokens(ScopeRead)
	second := h.tokens(ScopeRead, ScopeHijack)
	latestUpstream := h.provider.token
	requireStatus(t, h.refresh(first.RefreshToken, ""), 200)
	if h.provider.usedRefresh != latestUpstream {
		t.Fatal("first consent retained superseded upstream credential")
	}
	requireStatus(t, h.request("POST", "/mcp/oauth/revoke", url.Values{"client_id": {"desktop"}, "token": {second.RefreshToken}}), 200)
	if _, err := h.server.Authenticate(context.Background(), second.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoked token accepted")
	}
	if _, err := h.server.Authenticate(context.Background(), first.AccessToken); err != nil {
		t.Fatalf("other consent revoked: %v", err)
	}
}

func TestTemporaryRefreshFailureCanRetry(t *testing.T) {
	h := newHarness(t)
	tokens := h.tokens(ScopeRead)
	h.provider.failure = errors.New("identity provider unavailable")
	requireStatus(t, h.refresh(tokens.RefreshToken, ""), 503)
	h.provider.failure = nil
	requireStatus(t, h.refresh(tokens.RefreshToken, ""), 200)
}

func TestGrantResourceAndClientAreBound(t *testing.T) {
	h := newHarness(t)
	tokens := h.tokens(ScopeAdmin)
	p, err := h.server.Authenticate(context.Background(), tokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if p.HasScope(ScopeRead) || p.HasScope(ScopeHijack) {
		t.Fatal("admin category became a wildcard")
	}
	h.server.config.Resource = "https://jetbridge.example/another-mcp"
	if _, err := h.server.Authenticate(context.Background(), tokens.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("token accepted at different resource")
	}
}

func TestGrantManagementAndIdleExpiry(t *testing.T) {
	h := newHarness(t)
	tokens := h.tokens(ScopeRead)
	w := h.request("GET", "/mcp/oauth/grants", nil)
	requireStatus(t, w, 200)
	csrf, id := hidden(t, w.Body.String(), "csrf"), hidden(t, w.Body.String(), "grant_id")
	requireStatus(t, h.request("POST", "/mcp/oauth/grants", url.Values{"csrf": {"wrong"}, "grant_id": {id}}), 403)
	requireStatus(t, h.request("POST", "/mcp/oauth/grants", url.Values{"csrf": {csrf}, "grant_id": {id}}), 303)
	if _, err := h.server.Authenticate(context.Background(), tokens.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("management revoke did not invalidate access")
	}
	tokens = h.tokens(ScopeRead)
	h.now = h.now.Add(31 * 24 * time.Hour)
	requireStatus(t, h.refresh(tokens.RefreshToken, ""), 400)
}
