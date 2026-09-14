package skyserver

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/skymarshal/token"
	"golang.org/x/oauth2"
)

type SkyConfig struct {
	Logger          lager.Logger
	TokenMiddleware token.Middleware
	OAuthConfig     *oauth2.Config
	HTTPClient      *http.Client
	StateSigningKey []byte
	// RevokeRefresh is called only with a subject obtained from a successful,
	// client-bound token exchange against our configured issuer.
	RevokeRefresh        func(context.Context, string, string) error
	RefreshTokenLifetime time.Duration
}

func NewSkyHandler(server *SkyServer) http.Handler {
	handler := http.NewServeMux()
	handler.HandleFunc("/sky/login", server.Login)
	handler.HandleFunc("/sky/logout", server.Logout)
	handler.HandleFunc("/sky/callback", server.Callback)
	handler.HandleFunc("/sky/token/refresh", server.Refresh)
	return handler
}

func NewSkyServer(config *SkyConfig) (*SkyServer, error) {
	if len(config.StateSigningKey) < 32 {
		return nil, errors.New("StateSigningKey must be at least 32 bytes")
	}
	return &SkyServer{config}, nil
}

type SkyServer struct {
	config *SkyConfig
}

func (s *SkyServer) Login(w http.ResponseWriter, r *http.Request) {
	logger := s.config.Logger.Session("login")

	tokenString := s.config.TokenMiddleware.GetAuthToken(r)
	if tokenString == "" {
		s.NewLogin(w, r)
		return
	}

	redirectURI := r.FormValue("redirect_uri")
	if redirectURI == "" {
		redirectURI = "/"
	}

	parts := strings.Split(tokenString, " ")
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		logger.Info("failed-to-parse-cookie")
		s.NewLogin(w, r)
		return
	}

	expiry, err := parseJWTExpiry(parts[1])
	if err != nil {
		logger.Error("failed-to-parse-expiration", err)
		s.NewLogin(w, r)
		return
	}

	if time.Now().After(expiry) {
		logger.Info("token-is-expired")
		s.NewLogin(w, r)
		return
	}

	http.Redirect(w, r, redirectURI, http.StatusTemporaryRedirect)
}

func (s *SkyServer) NewLogin(w http.ResponseWriter, r *http.Request) {
	logger := s.config.Logger.Session("new-login")

	redirectURI := r.FormValue("redirect_uri")
	if redirectURI == "" {
		redirectURI = "/"
	}

	stateToken, err := s.signState(stateToken{
		RedirectURI: redirectURI,
		Entropy:     randomString(),
		Timestamp:   time.Now().Unix(),
	})
	if err != nil {
		logger.Error("failed-to-sign-state", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	authCodeURL := s.config.OAuthConfig.AuthCodeURL(stateToken, oauth2.AccessTypeOffline)

	http.Redirect(w, r, authCodeURL, http.StatusTemporaryRedirect)
}

func (s *SkyServer) Callback(w http.ResponseWriter, r *http.Request) {
	logger := s.config.Logger.Session("callback")

	if errMsg, errDesc := r.FormValue("error"), r.FormValue("error_description"); errMsg != "" {
		logger.Error("failed-with-callback-error", errors.New(errMsg+" : "+errDesc))
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}

	urlState := r.FormValue("state")
	state, err := s.verifyState(urlState)
	if err != nil {
		logger.Error("failed-to-verify-state", err)
		http.Error(w, "invalid state token", http.StatusBadRequest)
		return
	}

	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, s.config.HTTPClient)

	dexToken, err := s.config.OAuthConfig.Exchange(ctx, r.FormValue("code"))
	if err != nil {
		logger.Error("failed-to-fetch-dex-token", err)
		switch e := err.(type) {
		case *oauth2.RetrieveError:
			http.Error(w, string(e.Body), e.Response.StatusCode)
			return
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	s.Redirect(w, r, dexToken, state.RedirectURI)
}

func (s *SkyServer) Redirect(w http.ResponseWriter, r *http.Request, oauth2Token *oauth2.Token, redirectURI string) {
	logger := s.config.Logger.Session("redirect")

	redirectURL, err := url.ParseRequestURI("/" + strings.TrimLeft(redirectURI, "/"))
	if err != nil {
		logger.Error("failed-to-parse-redirect-url", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if redirectURL.Host != "" || redirectURL.Scheme != "" {
		logger.Error("invalid-redirect", fmt.Errorf("Unsupported redirect uri: %s", redirectURI))
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// Extract ID token from Dex's response — this is our bearer token
	idToken, _ := oauth2Token.Extra("id_token").(string)
	if idToken == "" {
		http.Error(w, "issuer response missing identity token", http.StatusBadGateway)
		return
	}

	err = s.config.TokenMiddleware.SetAuthToken(w, "bearer "+idToken, oauth2Token.Expiry)
	if err != nil {
		logger.Error("failed-to-set-auth-token", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Store refresh token in a separate cookie if present
	if oauth2Token.RefreshToken != "" {
		// Refresh tokens have longer lifetime — use 30 days as default
		refreshExpiry := time.Now().Add(s.refreshLifetime())
		err = s.config.TokenMiddleware.SetRefreshToken(w, oauth2Token.RefreshToken, refreshExpiry)
		if err != nil {
			logger.Error("failed-to-set-refresh-token", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	} else {
		// A connector without offline access must not inherit a previous
		// login's renewal cookie, potentially for a different account.
		s.config.TokenMiddleware.UnsetRefreshToken(w)
	}

	csrfToken := randomString()

	err = s.config.TokenMiddleware.SetCSRFToken(w, csrfToken, oauth2Token.Expiry)
	if err != nil {
		logger.Error("failed-to-set-state-token", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	params := redirectURL.Query()
	params.Set("csrf_token", csrfToken)

	http.Redirect(w, r, redirectURL.EscapedPath()+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (s *SkyServer) Refresh(w http.ResponseWriter, r *http.Request) {
	logger := s.config.Logger.Session("refresh")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	refreshToken := s.config.TokenMiddleware.GetRefreshToken(r)
	if refreshToken == "" {
		logger.Info("no-refresh-token")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if !s.sameOrigin(r) {
		http.Error(w, "same-origin request required", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.config.HTTPClient)

	// Exchange refresh token with Dex for new tokens
	newToken, err := s.config.OAuthConfig.TokenSource(ctx, &oauth2.Token{
		RefreshToken: refreshToken,
	}).Token()
	if err != nil {
		logger.Info("failed-to-refresh-token")
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && retrieve.Response != nil && retrieve.Response.StatusCode < 500 {
			w.WriteHeader(http.StatusUnauthorized)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}

	// Extract new ID token
	idToken, _ := newToken.Extra("id_token").(string)
	if idToken == "" {
		http.Error(w, "issuer response missing identity token", http.StatusBadGateway)
		return
	}

	err = s.config.TokenMiddleware.SetAuthToken(w, "bearer "+idToken, newToken.Expiry)
	if err != nil {
		logger.Error("failed-to-set-auth-token", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Rotate refresh token if Dex issued a new one
	if newToken.RefreshToken != "" {
		refreshExpiry := time.Now().Add(s.refreshLifetime())
		err = s.config.TokenMiddleware.SetRefreshToken(w, newToken.RefreshToken, refreshExpiry)
		if err != nil {
			logger.Error("failed-to-set-refresh-token", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}

	csrfToken := randomString()
	err = s.config.TokenMiddleware.SetCSRFToken(w, csrfToken, newToken.Expiry)
	if err != nil {
		logger.Error("failed-to-set-csrf-token", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"csrf_token": csrfToken,
	})
}

func (s *SkyServer) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	refresh := s.config.TokenMiddleware.GetRefreshToken(r)
	conf := *s.config.OAuthConfig
	if refresh != "" {
		if !s.sameOrigin(r) {
			http.Error(w, "same-origin request required", http.StatusForbidden)
			return
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		refresh = r.PostForm.Get("refresh_token")
		if refresh != "" {
			switch r.PostForm.Get("client_id") {
			case "fly":
				conf.ClientID, conf.ClientSecret = "fly", "Zmx5"
			case "fly-browser":
				conf.ClientID, conf.ClientSecret = "fly-browser", ""
			default:
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			conf.Endpoint.AuthStyle = oauth2.AuthStyleInParams
		}
	}
	// Clear local browser state even when remote revocation is unavailable, but
	// report that distinction instead of claiming a completed remote logout.
	s.config.TokenMiddleware.UnsetAuthToken(w)
	s.config.TokenMiddleware.UnsetCSRFToken(w)
	s.config.TokenMiddleware.UnsetRefreshToken(w)
	if refresh == "" {
		return
	}
	if s.config.RevokeRefresh == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, oauth2.HTTPClient, s.config.HTTPClient)
	refreshed, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		var retrieve *oauth2.RetrieveError
		if errors.As(err, &retrieve) && retrieve.Response != nil && retrieve.Response.StatusCode < 500 {
			// Already rejected/expired grants cannot renew and need no revocation.
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	idToken, _ := refreshed.Extra("id_token").(string)
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Subject string `json:"sub"`
	}
	if err != nil || json.Unmarshal(payload, &claims) != nil || claims.Subject == "" {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if err := s.config.RevokeRefresh(ctx, claims.Subject, conf.ClientID); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
	}
}

func (s *SkyServer) refreshLifetime() time.Duration {
	if s.config.RefreshTokenLifetime > 0 {
		return s.config.RefreshTokenLifetime
	}
	return 30 * 24 * time.Hour
}

// Origin remains available after the short-lived auth/CSRF cookies expire.
// Requiring it prevents cookie renewal or revocation through cross-site forms.
func (s *SkyServer) sameOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	if err != nil || origin.Host == "" || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return false
	}
	expected, err := url.Parse(s.config.OAuthConfig.RedirectURL)
	if err != nil {
		return false
	}
	if expected.Host == "" {
		expected.Host = r.Host
		expected.Scheme = "http"
		if r.TLS != nil {
			expected.Scheme = "https"
		}
	}
	return origin.Scheme == expected.Scheme && strings.EqualFold(origin.Host, expected.Host)
}

type stateToken struct {
	RedirectURI string `json:"redirect_uri"`
	Entropy     string `json:"entropy"`
	Timestamp   int64  `json:"ts"`
	Signature   string `json:"sig,omitempty"`
}

const stateTokenMaxAge = 3600 // 1 hour

func (s *SkyServer) signState(st stateToken) (string, error) {
	st.Signature = ""
	payload, err := json.Marshal(st)
	if err != nil {
		return "", err
	}

	mac := hmac.New(sha256.New, s.config.StateSigningKey)
	mac.Write(payload)
	st.Signature = base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	signed, err := json.Marshal(st)
	if err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(signed), nil
}

func (s *SkyServer) verifyState(raw string) (stateToken, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return stateToken{}, errors.New("failed to decode state")
	}

	var st stateToken
	if err := json.Unmarshal(data, &st); err != nil {
		return stateToken{}, errors.New("failed to unmarshal state")
	}

	sig := st.Signature
	if sig == "" {
		return stateToken{}, errors.New("missing signature")
	}

	st.Signature = ""
	payload, err := json.Marshal(st)
	if err != nil {
		return stateToken{}, errors.New("failed to marshal state for verification")
	}

	mac := hmac.New(sha256.New, s.config.StateSigningKey)
	mac.Write(payload)
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return stateToken{}, errors.New("signature mismatch")
	}

	if time.Now().Unix()-st.Timestamp > stateTokenMaxAge {
		return stateToken{}, errors.New("state expired")
	}

	return st, nil
}

// parseJWTExpiry extracts the expiry time from a JWT without verifying the signature.
// This is safe here because we only use it to decide whether to redirect to login
// (the actual signature verification happens in the API verifier).
func parseJWTExpiry(rawToken string) (time.Time, error) {
	parts := strings.Split(rawToken, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("invalid JWT format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("decode JWT payload: %w", err)
	}

	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("unmarshal JWT claims: %w", err)
	}

	if claims.Exp == 0 {
		return time.Time{}, errors.New("missing exp claim")
	}

	return time.Unix(int64(claims.Exp), 0), nil
}

func randomString() string {
	bytes := make([]byte, 32)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
