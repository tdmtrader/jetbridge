package mcpauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

type Server struct {
	config                   Config
	clients                  map[string]Client
	mux                      *http.ServeMux
	issuerPath, metadataPath string
}

type identitySession struct {
	Identity Identity
	Expires  time.Time
}
type grant struct {
	ID, ClientID, IdentityKey, Resource   string
	Scopes                                []string
	Created, IdleExpires, AbsoluteExpires time.Time
	Revoked                               bool
}
type authorizationRequest struct {
	ClientID, RedirectURI, State, Challenge, Verifier, BrowserHash, CSRF, IdentityKey, UserName string
	Scopes                                                                                      []string
	Expires                                                                                     time.Time
	Management                                                                                  bool
}
type authorizationCode struct {
	GrantID, ClientID, RedirectURI, Resource, Challenge string
	Expires                                             time.Time
}
type credential struct {
	GrantID string
	Expires time.Time
	Used    bool
}
type browserSession struct {
	IdentityKey, CSRF string
	Expires           time.Time
}

func NewServer(config Config) (*Server, error) {
	if config.Store == nil || config.Provider == nil {
		return nil, errors.New("MCP auth requires a store and identity provider")
	}
	issuer, err := validServerURL(config.Issuer)
	if err != nil {
		return nil, fmt.Errorf("MCP issuer: %w", err)
	}
	resource, err := validServerURL(config.Resource)
	if err != nil {
		return nil, fmt.Errorf("MCP resource: %w", err)
	}
	if issuer.Scheme != resource.Scheme || issuer.Host != resource.Host {
		return nil, errors.New("MCP issuer and resource must share an origin")
	}
	if issuer.Path == "" || strings.HasSuffix(issuer.Path, "/") {
		return nil, errors.New("MCP issuer must have a nonempty path without trailing slash")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.AccessLifetime == 0 {
		config.AccessLifetime = 15 * time.Minute
	}
	if config.IdleLifetime == 0 {
		config.IdleLifetime = 30 * 24 * time.Hour
	}
	if config.AbsoluteLifetime == 0 {
		config.AbsoluteLifetime = 90 * 24 * time.Hour
	}
	if config.AccessLifetime <= 0 || config.IdleLifetime < config.AccessLifetime || config.AbsoluteLifetime < config.IdleLifetime {
		return nil, errors.New("invalid MCP credential lifetimes")
	}
	config.SecureCookies = config.SecureCookies || issuer.Scheme == "https"
	s := &Server{config: config, clients: map[string]Client{}, mux: http.NewServeMux(), issuerPath: issuer.Path, metadataPath: "/.well-known/oauth-protected-resource" + resource.Path}
	for _, client := range config.Clients {
		if client.ID == "" || client.Name == "" || len(client.RedirectURIs) == 0 {
			return nil, errors.New("MCP clients need client_id, client_name and redirect_uris")
		}
		if _, exists := s.clients[client.ID]; exists {
			return nil, fmt.Errorf("duplicate MCP client %q", client.ID)
		}
		for _, redirect := range client.RedirectURIs {
			if _, err := validServerURL(redirect); err != nil {
				return nil, fmt.Errorf("MCP client %q redirect: %w", client.ID, err)
			}
		}
		s.clients[client.ID] = client
	}
	s.mux.HandleFunc(issuer.Path+"/authorize", s.authorize)
	s.mux.HandleFunc(issuer.Path+"/callback", s.callback)
	s.mux.HandleFunc(issuer.Path+"/consent", s.consent)
	s.mux.HandleFunc(issuer.Path+"/token", s.token)
	s.mux.HandleFunc(issuer.Path+"/revoke", s.revoke)
	s.mux.HandleFunc(issuer.Path+"/grants", s.grants)
	s.mux.HandleFunc("/.well-known/oauth-authorization-server"+issuer.Path, s.authorizationMetadata)
	s.mux.HandleFunc(s.metadataPath, s.resourceMetadata)
	return s, nil
}

func validServerURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return nil, errors.New("must be an absolute HTTPS URL without credentials, query or fragment")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, errors.New("HTTPS is required except on loopback")
	}
	return u, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	s.mux.ServeHTTP(w, r.WithContext(ctx))
}

func (s *Server) authorizationMetadata(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	scopes := []string{"offline_access"}
	for _, scope := range Scopes {
		scopes = append(scopes, scope.Name)
	}
	writeJSON(w, http.StatusOK, map[string]any{"issuer": s.config.Issuer, "authorization_endpoint": s.config.Issuer + "/authorize", "token_endpoint": s.config.Issuer + "/token", "revocation_endpoint": s.config.Issuer + "/revoke", "response_types_supported": []string{"code"}, "grant_types_supported": []string{"authorization_code", "refresh_token"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "revocation_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": scopes, "authorization_response_iss_parameter_supported": true})
}

func (s *Server) resourceMetadata(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resource": s.config.Resource, "authorization_servers": []string{s.config.Issuer}, "scopes_supported": scopeNames(), "bearer_methods_supported": []string{"header"}, "resource_name": "JetBridge MCP"})
}

func (s *Server) challenge(w http.ResponseWriter, status int, code, scope string) {
	resource, _ := url.Parse(s.config.Resource)
	header := fmt.Sprintf("Bearer resource_metadata=%q, scope=%q", resource.Scheme+"://"+resource.Host+s.metadataPath, scope)
	if code != "" {
		header += fmt.Sprintf(", error=%q", code)
	}
	w.Header().Set("WWW-Authenticate", header)
	writeJSON(w, status, map[string]string{"error": code})
}

// AuthorizeHTTP requires a dedicated MCP bearer credential on every request.
// Cookies and legacy fly/web credentials are intentionally not accepted here.
func (s *Server) AuthorizeHTTP(next func(http.ResponseWriter, *http.Request, Principal)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			s.challenge(w, 401, "invalid_token", ScopeRead)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		p, err := s.Authenticate(ctx, parts[1])
		cancel()
		if err != nil {
			if errors.Is(err, ErrInvalidToken) {
				s.challenge(w, 401, "invalid_token", ScopeRead)
			} else {
				oauthError(w, 503, "temporarily_unavailable", "Authorization storage unavailable")
			}
			return
		}
		next(w, r, p)
	})
}

func (s *Server) Authenticate(ctx context.Context, token string) (Principal, error) {
	var p Principal
	if !strings.HasPrefix(token, "jbm_a_") {
		return p, ErrInvalidToken
	}
	err := s.config.Store.WithTx(ctx, func(tx Tx) error {
		var c credential
		if err := tx.Get("access", digest(token), &c); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrInvalidToken
			}
			return err
		}
		var g grant
		if err := tx.Get("grant", c.GrantID, &g); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrInvalidToken
			}
			return err
		}
		now := s.config.Now()
		if !now.Before(c.Expires) || !g.valid(now) || g.Resource != s.config.Resource {
			return ErrInvalidToken
		}
		if _, ok := s.clients[g.ClientID]; !ok {
			return ErrInvalidToken
		}
		var identity identitySession
		if err := tx.Get("identity", g.IdentityKey, &identity); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrInvalidToken
			}
			return err
		}
		p = Principal{GrantID: g.ID, ClientID: g.ClientID, Scopes: g.Scopes, Claims: identity.Identity.Claims}
		return nil
	})
	return p, err
}

func (g grant) valid(now time.Time) bool {
	return !g.Revoked && now.Before(g.IdleExpires) && now.Before(g.AbsoluteExpires)
}
func digest(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func random() string { return base64.RawURLEncoding.EncodeToString(randomBytes()) }
func randomBytes() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}
func equal(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func method(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method != allowed {
		w.Header().Set("Allow", allowed)
		w.WriteHeader(405)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func oauthError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}
func identityKey(identity Identity) (string, error) {
	sub, _ := identity.Claims["sub"].(string)
	federated, _ := identity.Claims["federated_claims"].(map[string]any)
	connector, _ := federated["connector_id"].(string)
	if sub == "" || connector == "" {
		return "", errors.New("identity is missing subject or connector")
	}
	return digest(sub + "\x00" + connector), nil
}
func selectedScopes(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return []string{ScopeRead}, nil
	}
	var result []string
	for _, name := range strings.Fields(raw) {
		if name == "offline_access" {
			continue
		}
		if !slices.ContainsFunc(Scopes, func(s Scope) bool { return s.Name == name }) {
			return nil, errors.New("unsupported scope")
		}
		if !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("at least one capability is required")
	}
	return result, nil
}
