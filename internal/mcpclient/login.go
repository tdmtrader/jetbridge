package mcpclient

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
)

// Login opens an exact, pre-registered loopback listener. visit may open a real
// browser, print the URL, or drive a test browser. The authorization code is
// accepted only with matching state and authorization-server issuer.
func (c *Client) Login(ctx context.Context, visit func(string) error) error {
	redirect, err := url.Parse(c.config.RedirectURL)
	if err != nil || redirect.Scheme != "http" || redirect.Port() == "" || redirect.Port() == "0" || redirect.Path == "" || redirect.RawQuery != "" || redirect.Fragment != "" || redirect.User != nil {
		return errors.New("redirect must be a registered http://127.0.0.1:PORT/callback URL")
	}
	ip := net.ParseIP(redirect.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return errors.New("reference client callback must use a numeric loopback address")
	}
	metadata, err := c.Discover(ctx)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		return fmt.Errorf("listen on the registered callback: %w", err)
	}
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	codes, failures := make(chan string, 1), make(chan error, 1)
	var accepted atomic.Bool
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet || r.URL.Path != redirect.Path {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 || q.Get("iss") != metadata.Issuer {
			http.Error(w, "Invalid authorization callback", 400)
			return
		}
		if !accepted.CompareAndSwap(false, true) {
			http.Error(w, "Authorization already received", 409)
			return
		}
		if q.Get("error") != "" {
			failures <- errors.New("MCP authorization was declined")
			http.Error(w, "Authorization declined", 400)
			return
		}
		if q.Get("code") == "" {
			failures <- errors.New("authorization callback has no code")
			http.Error(w, "Missing code", 400)
			return
		}
		codes <- q.Get("code")
		fmt.Fprintln(w, "Authorization received. Return to the JetBridge client.")
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case failures <- errors.New("authorization callback listener stopped"):
			default:
			}
		}
	}()
	timeout, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	authURL := c.oauthConfig(metadata).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", c.config.Endpoint))
	if visit == nil {
		return errors.New("a browser authorization callback is required")
	}
	if err := visit(authURL); err != nil {
		return err
	}
	var code string
	select {
	case code = <-codes:
	case err := <-failures:
		return err
	case <-timeout.Done():
		return errors.New("MCP browser authorization timed out or was canceled")
	}
	exchange, cancelExchange := context.WithTimeout(timeout, 15*time.Second)
	defer cancelExchange()
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {c.config.ClientID}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {c.config.RedirectURL}, "resource": {c.config.Endpoint}}
	token, err := c.exchangeForm(exchange, metadata.TokenEndpoint, form)
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

// Logout revokes this grant and removes its local credentials. It distinguishes
// failed remote revocation from successful local cleanup.
func (c *Client) Logout(ctx context.Context) error {
	return c.withLock(ctx, func() error {
		state, err := c.load()
		if err != nil {
			return err
		}
		timeout, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		form := url.Values{"client_id": {c.config.ClientID}, "token": {state.Token.RefreshToken}, "token_type_hint": {"refresh_token"}}
		req, err := http.NewRequestWithContext(timeout, http.MethodPost, state.Metadata.RevocationEndpoint, strings.NewReader(form.Encode()))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, remoteErr := c.base.Do(req)
		if remoteErr != nil {
			remoteErr = errors.New("MCP revocation server could not be reached")
		} else {
			response.Body.Close()
			if response.StatusCode != 200 && response.StatusCode != 204 {
				remoteErr = fmt.Errorf("MCP revocation returned HTTP %d", response.StatusCode)
			}
		}
		if err := os.Remove(c.config.StatePath); err != nil {
			return errors.Join(errors.New("could not remove local MCP credentials"), err, remoteErr)
		}
		if remoteErr != nil {
			return fmt.Errorf("local MCP login removed; remote revocation was not confirmed: %w", remoteErr)
		}
		return nil
	})
}
