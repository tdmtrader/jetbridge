package mcpauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"golang.org/x/oauth2"
)

func TestOAuthProviderRequiresNonceAndFreshIdentity(t *testing.T) {
	verifier := random()
	nonce := digest(verifier)
	var grantType, receivedVerifier string
	upstreamError := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		grantType = r.Form.Get("grant_type")
		receivedVerifier = r.Form.Get("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		if upstreamError != "" {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": upstreamError})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "upstream-access", "token_type": "Bearer", "expires_in": 900, "id_token": "verified-by-test", "refresh_token": "upstream-refresh"})
	}))
	defer server.Close()
	provider := OAuthProvider{Config: &oauth2.Config{ClientID: "broker", ClientSecret: "secret", Endpoint: oauth2.Endpoint{AuthURL: server.URL + "/auth", TokenURL: server.URL + "/token"}, RedirectURL: "https://jetbridge.example/mcp/oauth/callback"}, VerifyIDToken: func(_ context.Context, raw string) (map[string]any, error) {
		if raw != "verified-by-test" {
			t.Fatal("provider did not verify returned identity token")
		}
		return map[string]any{"nonce": nonce}, nil
	}}
	parsed, _ := url.Parse(provider.AuthorizationURL("bound-state", verifier))
	if parsed.Query().Get("nonce") != nonce || parsed.Query().Get("code_challenge") != digest(verifier) || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatal("upstream login missing nonce or PKCE")
	}
	identity, err := provider.Exchange(context.Background(), "code", verifier)
	if err != nil || identity.RefreshToken != "upstream-refresh" || grantType != "authorization_code" || receivedVerifier != verifier {
		t.Fatalf("exchange: %#v, %v", identity, err)
	}
	nonce = "wrong"
	if _, err := provider.Exchange(context.Background(), "code", verifier); err == nil {
		t.Fatal("mismatched upstream nonce accepted")
	}
	if _, err := provider.Refresh(context.Background(), "upstream-refresh"); err != nil || grantType != "refresh_token" {
		t.Fatalf("refresh: %v, %s", err, grantType)
	}
	for _, upstreamError = range []string{"invalid_grant", "invalid_request"} {
		if _, err := provider.Refresh(context.Background(), "expired"); !errors.Is(err, ErrInvalidGrant) {
			t.Fatalf("invalid provider grant (%s) was not classified: %v", upstreamError, err)
		}
	}
}

func TestMetadataAndRedirectValidation(t *testing.T) {
	h := newHarness(t)
	w := h.request("GET", "/.well-known/oauth-authorization-server/mcp/oauth", nil)
	requireStatus(t, w, 200)
	var metadata map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &metadata)
	if metadata["issuer"] != h.server.config.Issuer || metadata["authorization_response_iss_parameter_supported"] != true {
		t.Fatal("incorrect issuer metadata")
	}
	w = h.request("GET", "/.well-known/oauth-protected-resource/api/v1/mcp", nil)
	requireStatus(t, w, 200)
	_ = json.Unmarshal(w.Body.Bytes(), &metadata)
	if scopes := metadata["scopes_supported"].([]any); len(scopes) != 1 || scopes[0] != ScopeRead {
		t.Fatal("resource discovery should request only read")
	}
	for _, redirect := range []string{"https://unregistered.example/callback", "http://127.0.0.1:7001/callback", "http://127.0.0.1:7000/callback/extra"} {
		q := url.Values{"client_id": {"desktop"}, "redirect_uri": {redirect}}
		w = h.request("GET", "/mcp/oauth/authorize?"+q.Encode(), nil)
		requireStatus(t, w, 400)
		if w.Header().Get("Location") != "" {
			t.Fatal("invalid redirect was followed")
		}
	}
	config := h.server.config
	config.Issuer = "http://public.example/mcp/oauth"
	if _, err := NewServer(config); err == nil {
		t.Fatal("public plaintext authorization accepted")
	}
}
