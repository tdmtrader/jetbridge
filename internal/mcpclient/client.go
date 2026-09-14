// Package mcpclient is the reference renewable client for JetBridge's registered
// public-client MCP profile. It uses the official SDK for the MCP protocol.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type Config struct {
	Endpoint, ClientID, RedirectURL, StatePath string
	Scopes                                     []string
	HTTPClient                                 *http.Client
}

type Metadata struct {
	Resource              string   `json:"resource"`
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RevocationEndpoint    string   `json:"revocation_endpoint"`
	PKCEMethods           []string `json:"code_challenge_methods_supported"`
}

type Client struct {
	config Config
	base   *http.Client
}
type savedGrant struct {
	Version  int          `json:"version"`
	Endpoint string       `json:"endpoint"`
	ClientID string       `json:"client_id"`
	Metadata Metadata     `json:"metadata"`
	Token    oauth2.Token `json:"token"`
}

func New(config Config) (*Client, error) {
	if _, err := secureURL(config.Endpoint); err != nil {
		return nil, fmt.Errorf("MCP endpoint: %w", err)
	}
	if config.ClientID == "" || config.StatePath == "" {
		return nil, errors.New("client ID and credential file are required")
	}
	if len(config.Scopes) == 0 {
		config.Scopes = []string{"read", "offline_access"}
	}
	base := http.DefaultClient
	if config.HTTPClient != nil {
		base = config.HTTPClient
	}
	clone := *base
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{config: config, base: &clone}, nil
}

func secureURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("expected an absolute HTTPS URL without credentials, query or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, errors.New("HTTPS required except on loopback")
	}
	return u, nil
}

var metadataParameter = regexp.MustCompile(`(?i)(?:^|[, ])resource_metadata="([^"]+)"`)

func (c *Client) Discover(ctx context.Context) (Metadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var result Metadata
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.Endpoint, nil)
	if err != nil {
		return result, err
	}
	response, err := c.base.Do(req)
	if err != nil {
		return result, errors.New("could not reach MCP endpoint for authentication discovery")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		return result, fmt.Errorf("expected MCP authentication challenge, got HTTP %d", response.StatusCode)
	}
	var metadataURL string
	for _, value := range response.Header.Values("WWW-Authenticate") {
		if match := metadataParameter.FindStringSubmatch(value); len(match) == 2 {
			metadataURL = match[1]
			break
		}
	}
	if metadataURL == "" {
		return result, errors.New("MCP challenge has no protected-resource metadata URL")
	}
	if _, err := secureURL(metadataURL); err != nil {
		return result, err
	}
	var resource struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
	}
	if err := c.getJSON(ctx, metadataURL, &resource); err != nil {
		return result, err
	}
	if resource.Resource != c.config.Endpoint || len(resource.AuthorizationServers) == 0 {
		return result, errors.New("protected-resource metadata does not identify this MCP endpoint")
	}
	issuer, err := secureURL(resource.AuthorizationServers[0])
	if err != nil {
		return result, err
	}
	discovery := issuer.Scheme + "://" + issuer.Host + "/.well-known/oauth-authorization-server" + issuer.EscapedPath()
	if err := c.getJSON(ctx, discovery, &result); err != nil {
		return result, err
	}
	if result.Issuer != resource.AuthorizationServers[0] {
		return result, errors.New("authorization server issuer mismatch")
	}
	result.Resource = resource.Resource
	if err := validateMetadata(result); err != nil {
		return result, err
	}
	return result, nil
}

func validateMetadata(metadata Metadata) error {
	issuer, err := secureURL(metadata.Issuer)
	if err != nil {
		return err
	}
	if !slices.Contains(metadata.PKCEMethods, "S256") {
		return errors.New("authorization server must advertise S256 PKCE support")
	}
	for _, endpoint := range []string{metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.RevocationEndpoint} {
		u, err := secureURL(endpoint)
		if err != nil {
			return err
		}
		if u.Scheme != issuer.Scheme || u.Host != issuer.Host {
			return errors.New("this registered-client profile requires authorization endpoints on the issuer origin")
		}
	}
	return nil
}

func (c *Client) getJSON(ctx context.Context, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.base.Do(req)
	if err != nil {
		return errors.New("authentication metadata is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("authentication metadata returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(out); err != nil {
		return errors.New("invalid authentication metadata")
	}
	return nil
}

func (c *Client) oauthConfig(m Metadata) *oauth2.Config {
	return &oauth2.Config{ClientID: c.config.ClientID, RedirectURL: c.config.RedirectURL, Scopes: c.config.Scopes,
		Endpoint: oauth2.Endpoint{AuthURL: m.AuthorizationEndpoint, TokenURL: m.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams}}
}

// SaveToken persists an already-authorized grant after verifying live discovery.
// Callers must never print the token or copy it into a URL.
func (c *Client) SaveToken(ctx context.Context, token *oauth2.Token) error {
	metadata, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	if err := validateToken(token); err != nil {
		return err
	}
	return c.withLock(ctx, func() error {
		return c.save(savedGrant{Version: 1, Endpoint: c.config.Endpoint, ClientID: c.config.ClientID, Metadata: metadata, Token: *token})
	})
}

func validateToken(token *oauth2.Token) error {
	if token == nil || token.AccessToken == "" || token.RefreshToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.Expiry.IsZero() {
		return errors.New("authorization server did not return a complete renewable bearer grant")
	}
	return nil
}

func (c *Client) load() (savedGrant, error) {
	var state savedGrant
	f, err := os.Open(c.config.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return state, errors.New("no saved MCP login; run connect")
	}
	if err != nil {
		return state, fmt.Errorf("read MCP credentials: %w", err)
	}
	defer f.Close()
	if err := json.NewDecoder(io.LimitReader(f, 64*1024)).Decode(&state); err != nil {
		return state, errors.New("invalid MCP credential file; run connect again")
	}
	if state.Version != 1 || state.Endpoint != c.config.Endpoint || state.ClientID != c.config.ClientID || state.Metadata.Resource != c.config.Endpoint {
		return state, errors.New("MCP credential file belongs to a different endpoint or client")
	}
	if err := validateMetadata(state.Metadata); err != nil {
		return state, fmt.Errorf("invalid saved authorization metadata; run connect again: %w", err)
	}
	return state, nil
}

func (c *Client) accessToken(ctx context.Context, rejected string) (string, error) {
	var access string
	err := c.withLock(ctx, func() error {
		state, err := c.load()
		if err != nil {
			return err
		}
		valid := state.Token.AccessToken != "" && time.Until(state.Token.Expiry) > 30*time.Second
		if valid && (rejected == "" || rejected != state.Token.AccessToken) {
			access = state.Token.AccessToken
			return nil
		}
		if state.Token.RefreshToken == "" {
			return errors.New("MCP login cannot renew; run connect again")
		}
		timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		// oauth2.TokenSource cannot attach the required resource parameter, so the
		// standard refresh form is sent explicitly to the discovered token endpoint.
		form := url.Values{"grant_type": {"refresh_token"}, "client_id": {c.config.ClientID}, "refresh_token": {state.Token.RefreshToken}, "resource": {c.config.Endpoint}}
		token, err := c.exchangeForm(timeout, state.Metadata.TokenEndpoint, form)
		if err != nil {
			return err
		}
		if token.RefreshToken == "" {
			token.RefreshToken = state.Token.RefreshToken
		}
		if err := validateToken(token); err != nil {
			return err
		}
		state.Token = *token
		if err := c.save(state); err != nil {
			return fmt.Errorf("MCP grant rotated but could not be saved; reconnect before further use: %w", err)
		}
		access = token.AccessToken
		return nil
	})
	return access, err
}

func (c *Client) exchangeForm(ctx context.Context, endpoint string, form url.Values) (*oauth2.Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.base.Do(req)
	if err != nil {
		return nil, errors.New("MCP authorization is temporarily unreachable; existing credentials were retained")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests {
			return nil, errors.New("MCP authorization is temporarily unavailable; existing credentials were retained")
		}
		return nil, errors.New("MCP authorization was rejected or expired; run connect again")
	}
	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		TokenType    string `json:"token_type"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&result) != nil || result.ExpiresIn <= 0 || result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") {
		return nil, errors.New("authorization server returned an invalid token response")
	}
	return &oauth2.Token{AccessToken: result.AccessToken, RefreshToken: result.RefreshToken, TokenType: result.TokenType, Expiry: time.Now().Add(time.Duration(result.ExpiresIn) * time.Second)}, nil
}

func (c *Client) HTTPClient() *http.Client {
	clone := *c.base
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = &authenticatedTransport{client: c, base: base}
	return &clone
}

type authenticatedTransport struct {
	client *Client
	base   http.RoundTripper
}

func (t *authenticatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.client.config.Endpoint {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, errors.New("refusing to send MCP credentials to a different resource")
	}
	token, err := t.client.accessToken(req.Context(), "")
	if err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	send := func(token string, body io.ReadCloser) (*http.Response, error) {
		r := req.Clone(req.Context())
		r.Header = req.Header.Clone()
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Del("Cookie")
		r.Body = body
		return t.base.RoundTrip(r)
	}
	response, err := send(token, req.Body)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	if req.Body != nil && req.GetBody == nil {
		return response, nil
	}
	response.Body.Close()
	token, err = t.client.accessToken(req.Context(), token)
	if err != nil {
		return nil, err
	}
	var body io.ReadCloser
	if req.GetBody != nil {
		body, err = req.GetBody()
		if err != nil {
			return nil, err
		}
	}
	return send(token, body)
}

func (c *Client) Connect(ctx context.Context) (*sdk.ClientSession, error) {
	client := sdk.NewClient(&sdk.Implementation{Name: "jetbridge-reference-client", Version: "1"}, nil)
	return client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: c.config.Endpoint, HTTPClient: c.HTTPClient()}, nil)
}
