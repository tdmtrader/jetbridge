package mcpauth

import (
	"context"
	"errors"
	"golang.org/x/oauth2"
	"net/http"
)

// OAuthProvider uses already-known Dex endpoints. VerifyIDToken may lazily
// discover verification keys; constructing the provider performs no network I/O.
type OAuthProvider struct {
	Config        *oauth2.Config
	HTTPClient    *http.Client
	VerifyIDToken func(context.Context, string) (map[string]any, error)
}

func (p OAuthProvider) AuthorizationURL(state, verifier string) string {
	return p.Config.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", digest(verifier)), oauth2.AccessTypeOffline)
}

func (p OAuthProvider) clientContext(ctx context.Context) context.Context {
	if p.HTTPClient != nil {
		return context.WithValue(ctx, oauth2.HTTPClient, p.HTTPClient)
	}
	return ctx
}

func (p OAuthProvider) Exchange(ctx context.Context, code, verifier string) (Identity, error) {
	tok, err := p.Config.Exchange(p.clientContext(ctx), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return Identity{}, err
	}
	identity, err := p.identity(ctx, tok)
	if err == nil && identity.Claims["nonce"] != digest(verifier) {
		return Identity{}, errors.New("identity nonce mismatch")
	}
	if err == nil && identity.RefreshToken == "" {
		return Identity{}, errors.New("identity provider did not issue a refresh credential")
	}
	return identity, err
}

func (p OAuthProvider) Refresh(ctx context.Context, refresh string) (Identity, error) {
	tok, err := p.Config.TokenSource(p.clientContext(ctx), &oauth2.Token{RefreshToken: refresh}).Token()
	if err != nil {
		var oauthErr *oauth2.RetrieveError
		// Pinned Dex reports missing/expired/replayed refresh credentials as
		// invalid_request, despite this being a well-formed refresh request.
		if errors.As(err, &oauthErr) && oauthErr.Response != nil && oauthErr.Response.StatusCode == http.StatusBadRequest && (oauthErr.ErrorCode == "invalid_grant" || oauthErr.ErrorCode == "invalid_request") {
			return Identity{}, ErrInvalidGrant
		}
		return Identity{}, err
	}
	identity, err := p.identity(ctx, tok)
	if identity.RefreshToken == "" {
		identity.RefreshToken = refresh
	}
	return identity, err
}

func (p OAuthProvider) identity(ctx context.Context, tok *oauth2.Token) (Identity, error) {
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" || p.VerifyIDToken == nil {
		return Identity{}, errors.New("identity provider response has no verifiable ID token")
	}
	claims, err := p.VerifyIDToken(ctx, raw)
	return Identity{Claims: claims, RefreshToken: tok.RefreshToken}, err
}
