package mcpclient

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type fixture struct {
	server *httptest.Server
	config Config
	token  http.HandlerFunc
	mcp    http.HandlerFunc
	pkce   []string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{pkce: []string{"S256"}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/mcp":
			if r.Header.Get("Authorization") == "" {
				w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.server.URL+`/.well-known/oauth-protected-resource/api/v1/mcp"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			f.mcp(w, r)
		case "/.well-known/oauth-protected-resource/api/v1/mcp":
			_ = json.NewEncoder(w).Encode(map[string]any{"resource": f.config.Endpoint, "authorization_servers": []string{f.server.URL + "/mcp/oauth"}})
		case "/.well-known/oauth-authorization-server/mcp/oauth":
			issuer := f.server.URL + "/mcp/oauth"
			_ = json.NewEncoder(w).Encode(Metadata{Issuer: issuer, AuthorizationEndpoint: issuer + "/authorize", TokenEndpoint: issuer + "/token", RevocationEndpoint: issuer + "/revoke", PKCEMethods: f.pkce})
		case "/mcp/oauth/token":
			f.token(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	f.config = Config{Endpoint: f.server.URL + "/api/v1/mcp", ClientID: "reference", StatePath: filepath.Join(t.TempDir(), "credentials.json")}
	t.Cleanup(f.server.Close)
	return f
}

func (f *fixture) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(f.config)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) save(t *testing.T, expiry time.Time) {
	t.Helper()
	err := f.client(t).SaveToken(context.Background(), &oauth2.Token{AccessToken: "access-one", RefreshToken: "refresh-one", TokenType: "Bearer", Expiry: expiry})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryRequiresAdvertisedS256(t *testing.T) {
	for _, methods := range [][]string{nil, {"plain"}} {
		f := newFixture(t)
		f.pkce = methods
		if _, err := f.client(t).Discover(context.Background()); err == nil || !strings.Contains(err.Error(), "S256") {
			t.Fatalf("discovery accepted PKCE methods %v: %v", methods, err)
		}
	}
}

func TestRejectedAccessTokenRenewsOnceAcrossClients(t *testing.T) {
	f := newFixture(t)
	var refreshes atomic.Int32
	f.token = func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.FormValue("grant_type") != "refresh_token" || r.FormValue("client_id") != "reference" || r.FormValue("resource") != f.config.Endpoint || r.FormValue("refresh_token") != "refresh-one" {
			t.Error("refresh request lost its client, resource or grant binding")
			w.WriteHeader(400)
			return
		}
		if refreshes.Add(1) != 1 {
			t.Error("rotated refresh token was reused")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-two", "refresh_token": "refresh-two", "token_type": "Bearer", "expires_in": 3600})
	}
	f.mcp = func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" {
			t.Error("MCP request forwarded a browser cookie")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"method":"tools/list"}` {
			t.Error("request body was lost on retry")
		}
		if r.Header.Get("Authorization") == "Bearer access-one" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer access-two" {
			t.Error("unexpected access credential")
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}
	f.save(t, time.Now().Add(time.Hour))
	var wg sync.WaitGroup
	for range 8 {
		// Each instance independently loads the persisted credential, as a fresh
		// invocation of the reference CLI does.
		client := f.client(t).HTTPClient()
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, _ := http.NewRequest("POST", f.config.Endpoint, strings.NewReader(`{"method":"tools/list"}`))
			r.Header.Set("Cookie", "auth=browser-session")
			response, err := client.Do(r)
			if err != nil {
				t.Error(err)
				return
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Errorf("MCP response: %d", response.StatusCode)
			}
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatalf("got %d refreshes, want one", refreshes.Load())
	}
	info, err := os.Stat(f.config.StatePath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential file must be private: %v, %v", info, err)
	}
	state, err := f.client(t).load()
	if err != nil || state.Token.RefreshToken != "refresh-two" {
		t.Fatal("a fresh client could not read the rotated credential")
	}
	if _, err := f.client(t).HTTPClient().Get(f.server.URL + "/another-resource"); err == nil {
		t.Fatal("client accepted a different resource URL")
	}
}

func TestTemporaryFailureRetainsRenewableLogin(t *testing.T) {
	f := newFixture(t)
	var unavailable atomic.Bool
	unavailable.Store(true)
	f.token = func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "upstream diagnostic contains refresh-one")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-two", "refresh_token": "refresh-two", "token_type": "Bearer", "expires_in": 3600})
	}
	f.mcp = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }
	f.save(t, time.Now().Add(-time.Minute))
	before, _ := os.ReadFile(f.config.StatePath)
	_, err := f.client(t).HTTPClient().Get(f.config.Endpoint)
	if err == nil || !strings.Contains(err.Error(), "temporarily unavailable") || strings.Contains(err.Error(), "refresh-one") {
		t.Fatalf("expected a redacted temporary failure, got %v", err)
	}
	after, _ := os.ReadFile(f.config.StatePath)
	if string(before) != string(after) {
		t.Fatal("temporary failure changed the saved login")
	}
	unavailable.Store(false)
	response, err := f.client(t).HTTPClient().Get(f.config.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("restarted client did not recover: HTTP %d", response.StatusCode)
	}
}

func TestBrowserLoginBindsPKCEStateIssuerAndResource(t *testing.T) {
	f := newFixture(t)
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.config.RedirectURL = "http://" + reserved.Addr().String() + "/callback"
	reserved.Close()
	var challenge string
	f.token = func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256([]byte(r.FormValue("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge || r.FormValue("code") != "code-one" || r.FormValue("client_id") != "reference" || r.FormValue("resource") != f.config.Endpoint || r.FormValue("redirect_uri") != f.config.RedirectURL {
			t.Error("authorization code exchange lost PKCE, client, redirect or resource binding")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-one", "refresh_token": "refresh-one", "token_type": "Bearer", "expires_in": 3600})
	}
	err = f.client(t).Login(context.Background(), func(raw string) error {
		authorize, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := authorize.Query()
		challenge = q.Get("code_challenge")
		if q.Get("code_challenge_method") != "S256" || challenge == "" || q.Get("resource") != f.config.Endpoint || q.Get("redirect_uri") != f.config.RedirectURL {
			t.Fatal("authorization request is missing its bindings")
		}
		callback, _ := url.Parse(f.config.RedirectURL)
		values := url.Values{"code": {"code-one"}, "state": {q.Get("state")}, "iss": {f.server.URL + "/mcp/oauth"}}
		call := func(expected int) error {
			callback.RawQuery = values.Encode()
			response, err := http.Get(callback.String())
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != expected {
				t.Errorf("callback HTTP %d, want %d", response.StatusCode, expected)
			}
			return nil
		}
		values.Set("state", "wrong-state")
		if err := call(400); err != nil {
			return err
		}
		values.Set("state", q.Get("state"))
		values.Set("iss", f.server.URL+"/wrong-issuer")
		if err := call(400); err != nil {
			return err
		}
		values.Set("iss", f.server.URL+"/mcp/oauth")
		return call(200)
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.client(t).load()
	if err != nil || state.Token.RefreshToken != "refresh-one" {
		t.Fatal("browser login was not persisted for the next invocation")
	}
}
