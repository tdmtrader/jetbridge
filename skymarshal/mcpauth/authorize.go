package mcpauth

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	q := r.URL.Query()
	client, ok := s.clients[q.Get("client_id")]
	if !ok || !slices.Contains(client.RedirectURIs, q.Get("redirect_uri")) {
		oauthError(w, 400, "invalid_request", "Unknown client or unregistered redirect URI")
		return
	}
	if q.Get("response_type") != "code" || q.Get("resource") != s.config.Resource || q.Get("code_challenge_method") != "S256" || !validChallenge(q.Get("code_challenge")) {
		oauthError(w, 400, "invalid_request", "Authorization requires code, the MCP resource, and S256 PKCE")
		return
	}
	scopes, err := selectedScopes(q.Get("scope"))
	if err != nil {
		oauthError(w, 400, "invalid_scope", err.Error())
		return
	}
	req := authorizationRequest{ClientID: client.ID, RedirectURI: q.Get("redirect_uri"), State: q.Get("state"), Challenge: q.Get("code_challenge"), Scopes: scopes}
	s.begin(w, r, req)
}

func validChallenge(challenge string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return err == nil && len(decoded) == 32 && len(challenge) == 43
}

func validVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, c := range verifier {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
			return false
		}
	}
	return true
}

func (s *Server) begin(w http.ResponseWriter, r *http.Request, req authorizationRequest) {
	id, browser := random(), random()
	req.BrowserHash = digest(browser)
	req.CSRF = random()
	req.Verifier = random()
	req.Expires = s.config.Now().Add(10 * time.Minute)
	err := s.config.Store.WithTx(r.Context(), func(tx Tx) error { return tx.Put("request", digest(id), req, req.Expires) })
	if err != nil {
		oauthError(w, 503, "temporarily_unavailable", "Could not start authorization")
		return
	}
	s.setCookie(w, requestCookie(id), browser, req.Expires)
	http.Redirect(w, r, s.config.Provider.AuthorizationURL(id, req.Verifier), http.StatusSeeOther)
}

func requestCookie(id string) string { return "jbm_request_" + digest(id)[:12] }
func (s *Server) setCookie(w http.ResponseWriter, name, value string, expiry time.Time) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: s.issuerPath, Expires: expiry, HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode})
}
func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: s.issuerPath, MaxAge: -1, HttpOnly: true, Secure: s.config.SecureCookies, SameSite: http.SameSiteLaxMode})
}
func (s *Server) request(tx Tx, r *http.Request, id string) (authorizationRequest, error) {
	var req authorizationRequest
	if id == "" {
		return req, ErrInvalidGrant
	}
	if err := tx.Get("request", digest(id), &req); err != nil {
		return req, err
	}
	cookie, err := r.Cookie(requestCookie(id))
	if err != nil || !equal(req.BrowserHash, digest(cookie.Value)) || !s.config.Now().Before(req.Expires) {
		return req, ErrInvalidGrant
	}
	return req, nil
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	id := r.URL.Query().Get("state")
	var req authorizationRequest
	var browserID string
	err := s.config.Store.WithTx(r.Context(), func(tx Tx) error {
		var err error
		req, err = s.request(tx, r, id)
		if err != nil {
			return err
		}
		if req.IdentityKey != "" || r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
			return ErrInvalidGrant
		}
		// Dex permits one refresh credential per subject/client. Serialize
		// callback exchanges so concurrent logins cannot persist an older
		// credential after a newer exchange has superseded it upstream.
		if err := tx.Lock("provider-login", false); err != nil {
			return err
		}
		identity, err := s.config.Provider.Exchange(r.Context(), r.URL.Query().Get("code"), req.Verifier)
		if err != nil {
			return err
		}
		key, err := identityKey(identity)
		if err != nil {
			return err
		}
		if identity.RefreshToken == "" {
			return errors.New("provider has no renewable identity")
		}
		// All consents for this subject share the most recent upstream refresh
		// credential. A new Dex login replaces that credential, not old consents.
		var previous identitySession
		if err := tx.Get("identity", key, &previous); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		expiry := s.config.Now().Add(s.config.AbsoluteLifetime)
		if err := tx.Put("identity", key, identitySession{Identity: identity, Expires: expiry}, expiry); err != nil {
			return err
		}
		req.IdentityKey = key
		for _, field := range []string{"preferred_username", "name", "email", "sub"} {
			if value, ok := identity.Claims[field].(string); ok && value != "" {
				req.UserName = value
				break
			}
		}
		req.Verifier = ""
		if err := tx.Put("request", digest(id), req, req.Expires); err != nil {
			return err
		}
		browserID = random()
		b := browserSession{IdentityKey: key, CSRF: random(), Expires: s.config.Now().Add(time.Hour)}
		return tx.Put("browser", digest(browserID), b, b.Expires)
	})
	if err != nil {
		http.Error(w, "Sign-in could not be completed. Restart authorization to try again.", 400)
		return
	}
	s.setCookie(w, "jbm_browser", browserID, s.config.Now().Add(time.Hour))
	if req.Management {
		_ = s.config.Store.WithTx(r.Context(), func(tx Tx) error { return tx.Delete("request", digest(id)) })
		s.clearCookie(w, requestCookie(id))
		http.Redirect(w, r, s.issuerPath+"/grants", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, s.issuerPath+"/consent?request="+url.QueryEscape(id), http.StatusSeeOther)
}

func (s *Server) consent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		w.WriteHeader(405)
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "Malformed consent")
		return
	}
	id := r.Form.Get("request")
	var req authorizationRequest
	var code string
	denied := false
	err := s.config.Store.WithTx(r.Context(), func(tx Tx) error {
		var err error
		req, err = s.request(tx, r, id)
		if err != nil {
			return err
		}
		if req.IdentityKey == "" || req.Management {
			return ErrInvalidGrant
		}
		if r.Method == http.MethodGet {
			return nil
		}
		if !equal(req.CSRF, r.PostForm.Get("csrf")) {
			return ErrInvalidGrant
		}
		if r.PostForm.Get("decision") == "deny" {
			denied = true
			return tx.Delete("request", digest(id))
		}
		if r.PostForm.Get("decision") != "allow" {
			return ErrInvalidGrant
		}
		var scopes []string
		for _, scope := range r.PostForm["scope"] {
			if !slices.Contains(req.Scopes, scope) {
				return ErrInvalidGrant
			}
			if !slices.Contains(scopes, scope) {
				scopes = append(scopes, scope)
			}
		}
		if len(scopes) == 0 {
			return ErrInvalidGrant
		}
		now := s.config.Now()
		g := grant{ID: random(), ClientID: req.ClientID, IdentityKey: req.IdentityKey, Resource: s.config.Resource, Scopes: scopes, Created: now, IdleExpires: now.Add(s.config.IdleLifetime), AbsoluteExpires: now.Add(s.config.AbsoluteLifetime)}
		if err := tx.Put("grant", g.ID, g, g.AbsoluteExpires); err != nil {
			return err
		}
		code = "jbm_c_" + random()
		c := authorizationCode{GrantID: g.ID, ClientID: g.ClientID, RedirectURI: req.RedirectURI, Resource: s.config.Resource, Challenge: req.Challenge, Expires: now.Add(5 * time.Minute)}
		if err := tx.Put("code", digest(code), c, c.Expires); err != nil {
			return err
		}
		return tx.Delete("request", digest(id))
	})
	if err != nil {
		http.Error(w, "Consent is invalid or expired. Select at least one requested capability, or restart authorization.", 400)
		return
	}
	if r.Method == http.MethodGet {
		client, ok := s.clients[req.ClientID]
		if !ok {
			http.Error(w, "Client is no longer registered", 400)
			return
		}
		s.renderConsent(w, id, req, client)
		return
	}
	s.clearCookie(w, requestCookie(id))
	redirect, _ := url.Parse(req.RedirectURI)
	q := redirect.Query()
	q.Set("state", req.State)
	q.Set("iss", s.config.Issuer)
	if denied {
		q.Set("error", "access_denied")
	} else {
		q.Set("code", code)
	}
	redirect.RawQuery = q.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}
