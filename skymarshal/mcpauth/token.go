package mcpauth

import (
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "Malformed token request")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if _, ok := s.clients[clientID]; !ok {
		oauthError(w, 400, "invalid_client", "Unknown public client")
		return
	}
	if r.PostForm.Get("resource") != s.config.Resource {
		oauthError(w, 400, "invalid_target", "The resource must identify this MCP endpoint")
		return
	}
	var response tokenResponse
	var grantError error
	err := s.config.Store.WithTx(r.Context(), func(tx Tx) error {
		var g grant
		switch r.PostForm.Get("grant_type") {
		case "authorization_code":
			var c authorizationCode
			if err := tx.Get("code", digest(r.PostForm.Get("code")), &c); err != nil {
				return err
			}
			verifier := r.PostForm.Get("code_verifier")
			if !s.config.Now().Before(c.Expires) || c.ClientID != clientID || c.Resource != s.config.Resource || c.RedirectURI != r.PostForm.Get("redirect_uri") || !validVerifier(verifier) || !equal(c.Challenge, digest(verifier)) {
				return ErrInvalidGrant
			}
			if err := tx.Get("grant", c.GrantID, &g); err != nil {
				return err
			}
			if !g.valid(s.config.Now()) || g.Resource != s.config.Resource {
				return ErrInvalidGrant
			}
			if err := tx.Delete("code", digest(r.PostForm.Get("code"))); err != nil {
				return err
			}
		case "refresh_token":
			raw := r.PostForm.Get("refresh_token")
			var c credential
			if err := tx.Get("refresh", digest(raw), &c); err != nil {
				return err
			}
			if err := tx.Get("grant", c.GrantID, &g); err != nil {
				return err
			}
			if g.ClientID != clientID || g.Resource != s.config.Resource || !g.valid(s.config.Now()) || !s.config.Now().Before(c.Expires) {
				return ErrInvalidGrant
			}
			if c.Used {
				// Commit revocation before returning invalid_grant. A replayed
				// credential must not leave its rotated successor usable.
				g.Revoked = true
				grantError = ErrInvalidGrant
				return tx.Put("grant", g.ID, g, g.AbsoluteExpires)
			}
			if rawScope := r.PostForm.Get("scope"); rawScope != "" {
				scopes, err := selectedScopes(rawScope)
				if err != nil {
					return ErrInvalidGrant
				}
				for _, scope := range scopes {
					if !slices.Contains(g.Scopes, scope) {
						return ErrInvalidGrant
					}
				}
				g.Scopes = scopes
			}
			// A single upstream identity session is shared by independent
			// consents. This lock serializes Dex's rotating refresh credential.
			// Shared locking permits unrelated identities to refresh concurrently,
			// while a new broker login cannot replace a credential mid-refresh.
			if err := tx.Lock("provider-login", true); err != nil {
				return err
			}
			var session identitySession
			if err := tx.Get("identity", g.IdentityKey, &session); err != nil {
				return err
			}
			identity, err := s.config.Provider.Refresh(r.Context(), session.Identity.RefreshToken)
			if err != nil {
				return err
			}
			key, err := identityKey(identity)
			if err != nil || key != g.IdentityKey {
				return ErrInvalidGrant
			}
			if identity.RefreshToken == "" {
				return ErrInvalidGrant
			}
			session.Identity = identity
			session.Expires = s.config.Now().Add(s.config.AbsoluteLifetime)
			if err := tx.Put("identity", key, session, session.Expires); err != nil {
				return err
			}
			c.Used = true
			if err := tx.Put("refresh", digest(raw), c, g.AbsoluteExpires); err != nil {
				return err
			}
			g.IdleExpires = minTime(s.config.Now().Add(s.config.IdleLifetime), g.AbsoluteExpires)
			if err := tx.Put("grant", g.ID, g, g.AbsoluteExpires); err != nil {
				return err
			}
		default:
			grantError = errUnsupportedGrant
			return nil
		}
		var err error
		response, err = s.issue(tx, g)
		return err
	})
	if grantError != nil {
		err = grantError
	}
	if err != nil {
		switch {
		case errors.Is(err, errUnsupportedGrant):
			oauthError(w, 400, "unsupported_grant_type", "Use authorization_code or refresh_token")
		case errors.Is(err, ErrInvalidGrant), errors.Is(err, ErrNotFound):
			oauthError(w, 400, "invalid_grant", "Credential is invalid, expired, revoked or already used")
		default:
			oauthError(w, 503, "temporarily_unavailable", "Identity renewal or authorization storage is unavailable; retry later")
		}
		return
	}
	writeJSON(w, 200, response)
}

var errUnsupportedGrant = errors.New("unsupported grant type")

func (s *Server) issue(tx Tx, g grant) (tokenResponse, error) {
	now := s.config.Now()
	expires := minTime(now.Add(s.config.AccessLifetime), minTime(g.IdleExpires, g.AbsoluteExpires))
	access, refresh := "jbm_a_"+random(), "jbm_r_"+random()
	if err := tx.Put("access", digest(access), credential{GrantID: g.ID, Expires: expires}, expires); err != nil {
		return tokenResponse{}, err
	}
	if err := tx.Put("refresh", digest(refresh), credential{GrantID: g.ID, Expires: g.AbsoluteExpires}, g.AbsoluteExpires); err != nil {
		return tokenResponse{}, err
	}
	return tokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(expires.Sub(now).Seconds()), RefreshToken: refresh, Scope: strings.Join(g.Scopes, " ")}, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if err := r.ParseForm(); err != nil {
		oauthError(w, 400, "invalid_request", "Malformed revocation request")
		return
	}
	clientID := r.PostForm.Get("client_id")
	if _, ok := s.clients[clientID]; !ok {
		oauthError(w, 400, "invalid_client", "Unknown public client")
		return
	}
	raw := r.PostForm.Get("token")
	kind := "refresh"
	if strings.HasPrefix(raw, "jbm_a_") {
		kind = "access"
	}
	err := s.config.Store.WithTx(r.Context(), func(tx Tx) error {
		var c credential
		if err := tx.Get(kind, digest(raw), &c); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		var g grant
		if err := tx.Get("grant", c.GrantID, &g); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		if g.ClientID != clientID {
			return nil
		}
		g.Revoked = true
		return tx.Put("grant", g.ID, g, g.AbsoluteExpires)
	})
	if err != nil {
		oauthError(w, 503, "temporarily_unavailable", "Revocation storage unavailable")
		return
	}
	w.WriteHeader(http.StatusOK)
}
