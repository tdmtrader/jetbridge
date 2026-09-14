package rc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

const FlyBrowserClientID = "fly-browser"

// OAuthClient is deliberately derived from a fixed client allowlist and the
// target URL. Local metadata can select a client, never another token endpoint.
func OAuthClient(targetAPI, clientID string) (*oauth2.Config, error) {
	secret := ""
	switch clientID {
	case "fly":
		secret = "Zmx5"
	case FlyBrowserClientID:
	default:
		return nil, errors.New("unknown login client; run fly login again")
	}
	return &oauth2.Config{
		ClientID: clientID, ClientSecret: secret,
		Endpoint: oauth2.Endpoint{AuthURL: strings.TrimRight(targetAPI, "/") + "/sky/issuer/auth", TokenURL: strings.TrimRight(targetAPI, "/") + "/sky/issuer/token", AuthStyle: oauth2.AuthStyleInParams},
		Scopes:   []string{"openid", "profile", "email", "federated:id", "groups", "offline_access"},
	}, nil
}

type refreshingTokenSource struct {
	mu         sync.Mutex
	token      *TargetToken
	targetName TargetName
	targetAPI  string
	base       http.RoundTripper
}

func newRefreshingTokenSource(token *TargetToken, name TargetName, api string, base http.RoundTripper) oauth2.TokenSource {
	copy := *token
	return &refreshingTokenSource{token: &copy, targetName: name, targetAPI: api, base: base}
}

type legacyTokenSource struct{ token TargetToken }

func (s legacyTokenSource) Token() (*oauth2.Token, error) {
	if expiry, err := parseJWTExpiry(s.token.Value); err == nil && !expiry.After(time.Now()) {
		return nil, errors.New("login expired and has no refresh credential; run fly login again")
	}
	return bearer(&s.token), nil
}

// CurrentTargetToken also covers callers (curl and the hijack websocket) which
// need a bearer outside the normal HTTP transport.
func CurrentTargetToken(target Target) (*TargetToken, error) {
	if transport, ok := target.Client().HTTPClient().Transport.(*oauth2.Transport); ok {
		value, err := transport.Source.Token()
		if err != nil {
			return nil, err
		}
		return &TargetToken{Type: value.TokenType, Value: value.AccessToken}, nil
	}
	return target.Token(), nil
}

func AuthHTTPClient(client *http.Client) *http.Client {
	copy := *client
	copy.Timeout = 15 * time.Second
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

func (s *refreshingTokenSource) Token() (*oauth2.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == nil || s.token.Value == "" {
		return nil, errors.New("not logged in; run fly login")
	}
	expiry, err := parseJWTExpiry(s.token.Value)
	if err != nil {
		return nil, errors.New("invalid saved login token; run fly login again")
	}
	if time.Until(expiry) > 30*time.Second {
		return bearer(s.token), nil
	}
	err = withTargetsLock(func() error {
		targets, err := LoadTargets()
		if err != nil {
			return fmt.Errorf("read login before renewal: %w", err)
		}
		props, ok := targets[s.targetName]
		if !ok || props.Token == nil || props.Token.Value == "" {
			return errors.New("target was logged out or removed; run fly login")
		}
		if props.API != s.targetAPI {
			return errors.New("target URL changed; reload the target before renewing")
		}
		current := *props.Token
		expiry, err := parseJWTExpiry(current.Value)
		if err != nil {
			return errors.New("invalid saved login token; run fly login again")
		}
		if current.Value != s.token.Value && time.Until(expiry) > 0 {
			s.token = &current
			return nil
		}
		clientID := current.OAuthClientID
		if clientID == "" {
			clientID = "fly"
		} // only legacy password login obtained refresh tokens
		conf, err := OAuthClient(s.targetAPI, clientID)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, oauth2.HTTPClient, AuthHTTPClient(&http.Client{Transport: s.base}))
		result, err := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: current.RefreshToken}).Token()
		if err != nil {
			return RenewalError(err)
		}
		renewed, err := TargetTokenFromOAuth(result, clientID)
		if err != nil {
			return err
		}
		if renewed.RefreshToken == "" {
			renewed.RefreshToken = current.RefreshToken
		}
		props.Token = renewed
		targets[s.targetName] = props
		if err := writeTargets(flyrcPath(), targets); err != nil {
			return fmt.Errorf("login renewed but could not save rotated credentials; retry promptly or run fly login again: %w", err)
		}
		s.token = renewed
		return nil
	})
	if err != nil {
		return nil, err
	}
	return bearer(s.token), nil
}

func bearer(t *TargetToken) *oauth2.Token {
	return &oauth2.Token{TokenType: t.Type, AccessToken: t.Value}
}

// TargetTokenFromOAuth validates the issuer response, retaining its ID token for
// the existing JetBridge API bearer convention. MCP uses a separate contract.
func TargetTokenFromOAuth(t *oauth2.Token, clientID string) (*TargetToken, error) {
	id, _ := t.Extra("id_token").(string)
	expiry, err := parseJWTExpiry(id)
	if err != nil || !expiry.After(time.Now()) || !strings.EqualFold(t.TokenType, "bearer") || t.AccessToken == "" {
		return nil, errors.New("issuer returned an invalid login response; no credentials were saved")
	}
	return &TargetToken{Type: "bearer", Value: id, RefreshToken: t.RefreshToken, OAuthClientID: clientID}, nil
}

// Do not include issuer bodies: they can contain tokens, connector data or credentials.
func RenewalError(err error) error {
	var response *oauth2.RetrieveError
	if errors.As(err, &response) && response.Response != nil && response.Response.StatusCode < 500 {
		return errors.New("login renewal was rejected or expired; run fly login again")
	}
	return errors.New("login renewal is temporarily unavailable; retry when the server is reachable")
}

func SaveTargetToken(name TargetName, token *TargetToken) error {
	return updateTargets(func(targets Targets) error {
		props, ok := targets[name]
		if !ok {
			return UnknownTargetError{name}
		}
		props.Token = token
		targets[name] = props
		return nil
	})
}

func parseJWTExpiry(raw string) (time.Time, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, errors.New("no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}
