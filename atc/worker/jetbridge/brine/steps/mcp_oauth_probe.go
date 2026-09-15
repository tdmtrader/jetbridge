package steps

// This file is fixture/probe-only. It exposes the existing real Dex/PostgreSQL
// authentication apparatus to hack/mcp-oauth-probe without copying auth logic or
// adding any production route. The observer records selected public metadata;
// request bodies, authorization codes, cookies and credentials never escape it.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/skymarshal/mcpauth"
)

type MCPOAuthProbe struct {
	scenario        *MCPScenario
	clientID        string
	browser         *http.Client
	dispose         []func() error
	mu              sync.Mutex
	events          []map[string]any
	previousRefresh string
}

// NewMCPOAuthProbe creates an isolated real database, issuer and API fixture.
// The supplied public client has one exact callback registration.
func NewMCPOAuthProbe(clientID, callback string) (probe *MCPOAuthProbe, err error) {
	p := &MCPOAuthProbe{clientID: clientID}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("probe fixture initialization failed: %v", recovered)
		}
		if err != nil {
			err = errors.Join(err, p.Close())
		}
	}()
	RegisterGomegaFailHandler()
	definitions := map[string]brine.ResourceDefinition{}
	for _, definition := range ResourceDefinitions() {
		definitions[definition.Name] = definition
	}
	values := map[string]any{}
	for _, name := range []string{"postgres", "jetbridge-db", "auth-binaries", "auth-server"} {
		definition := definitions[name]
		value, createErr := definition.Factory(values)
		if createErr != nil {
			return nil, fmt.Errorf("create %s: %w", name, createErr)
		}
		values[name] = value
		p.dispose = append(p.dispose, func() error { return definition.Disposer(value) })
	}
	p.scenario, err = newMCPScenario(values["auth-server"].(*AuthFixture))
	if err != nil {
		return nil, err
	}
	p.scenario.Config.Clients = []mcpauth.Client{{ID: clientID, Name: "Codex local OAuth probe", RedirectURIs: []string{callback}}}
	if err = p.scenario.install(); err != nil {
		return nil, err
	}
	f := p.scenario.Fixture
	f.mu.Lock()
	next := f.extra
	f.extra = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { p.observe(next, w, r) })
	f.mu.Unlock()
	return p, nil
}

func (p *MCPOAuthProbe) Endpoint() string { return p.scenario.Config.Resource }

func (p *MCPOAuthProbe) Close() error {
	var err error
	for i := len(p.dispose) - 1; i >= 0; i-- {
		err = errors.Join(err, p.dispose[i]())
	}
	p.dispose = nil
	return err
}

// Approve uses only the fixture owner and the actual issuer/consent HTML forms.
func (p *MCPOAuthProbe) Approve(start string, scopes []string) error {
	u, err := url.Parse(start)
	if err != nil {
		return errors.New("invalid probe authorization URL")
	}
	expected, _ := url.Parse(p.scenario.Config.Issuer + "/authorize")
	if u.Scheme != expected.Scheme || u.Host != expected.Host || u.Path != expected.Path || u.Query().Get("client_id") != p.clientID {
		return errors.New("authorization URL is outside the isolated probe client")
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	browser := &http.Client{Jar: jar, Timeout: 15 * time.Second}
	resp, err := p.scenario.Fixture.loginForm(browser, start, "owner")
	if err != nil {
		return errors.New("fixture issuer login failed")
	}
	body, err := authReadBody(resp)
	if err != nil {
		return errors.New("read fixture consent failed")
	}
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/mcp/oauth/consent" {
		return fmt.Errorf("fixture consent HTTP %d", resp.StatusCode)
	}
	action, form, err := authForm(body, resp.Request.URL)
	if err != nil {
		return errors.New("parse fixture consent failed")
	}
	form.Set("decision", "allow")
	for _, scope := range scopes {
		form.Add("scope", scope)
	}
	resp, err = browser.PostForm(action, form)
	if err != nil {
		return errors.New("fixture consent callback failed")
	}
	_, _ = authReadBody(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("client callback HTTP %d", resp.StatusCode)
	}
	p.browser = browser
	return nil
}

// Advance expires server-side access credentials through the existing test clock.
func (p *MCPOAuthProbe) Advance(duration time.Duration) { p.scenario.ClockOffset.Add(int64(duration)) }

// Revoke uses the real browser-management route, with its session and CSRF checks.
func (p *MCPOAuthProbe) Revoke() (int, error) {
	if p.browser == nil {
		return 0, errors.New("probe has no authenticated fixture browser")
	}
	target := p.scenario.Config.Issuer + "/grants"
	req, _ := http.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := p.browser.Do(req)
	if err != nil {
		return 0, errors.New("read fixture grants failed")
	}
	body, err := authReadBody(resp)
	if err != nil || resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fixture grants HTTP %d", resp.StatusCode)
	}
	var listing struct {
		Grants []mcpauth.GrantInfo `json:"grants"`
		CSRF   string              `json:"csrf_token"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		return 0, err
	}
	count := 0
	for _, grant := range listing.Grants {
		if grant.ClientID != p.clientID {
			continue
		}
		resp, err = p.browser.PostForm(target, url.Values{"csrf": {listing.CSRF}, "grant_id": {grant.ID}})
		if err != nil {
			return count, errors.New("revoke fixture grant failed")
		}
		_, _ = authReadBody(resp)
		if resp.StatusCode != http.StatusOK {
			return count, fmt.Errorf("revoke fixture grant HTTP %d", resp.StatusCode)
		}
		count++
	}
	return count, nil
}

func (p *MCPOAuthProbe) Events() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := append([]map[string]any(nil), p.events...)
	return result
}

func (p *MCPOAuthProbe) observe(next http.Handler, w http.ResponseWriter, r *http.Request) {
	event := map[string]any{"path": r.URL.Path, "http_method": r.Method}
	if r.URL.Path == "/mcp/oauth/authorize" {
		q := r.URL.Query()
		event["client_id"], event["redirect_uri"], event["requested_scope"] = q.Get("client_id"), q.Get("redirect_uri"), q.Get("scope")
		event["pkce_method"] = q.Get("code_challenge_method")
		event["resource_matches"] = q.Get("resource") == p.Endpoint()
	}
	var body []byte
	var err error
	if r.Body != nil {
		body, err = io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
	}
	if err != nil {
		http.Error(w, "probe request read failed", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var rpc struct {
		Method string `json:"method"`
		Params struct {
			Protocol  string `json:"protocolVersion"`
			Name      string `json:"name"`
			Arguments struct {
				Request struct {
					Operation string `json:"operation"`
				} `json:"request"`
			} `json:"arguments"`
		} `json:"params"`
	}
	if r.URL.Path == "/api/v1/mcp" {
		_ = json.Unmarshal(body, &rpc)
		event["rpc_method"], event["protocol_header"] = rpc.Method, r.Header.Get("MCP-Protocol-Version")
		if rpc.Method == "initialize" {
			event["requested_protocol"] = rpc.Params.Protocol
		}
		if rpc.Method == "tools/call" {
			event["tool"], event["operation"] = rpc.Params.Name, rpc.Params.Arguments.Request.Operation
		}
	}
	if r.URL.Path == "/mcp/oauth/token" {
		form, _ := url.ParseQuery(string(body))
		event["grant_type"], event["client_id"] = form.Get("grant_type"), form.Get("client_id")
		event["resource_matches"] = form.Get("resource") == p.Endpoint()
	}
	recorder := httptest.NewRecorder()
	next.ServeHTTP(recorder, r)
	event["status"] = recorder.Code
	if r.URL.Path == "/mcp/oauth/token" {
		var token struct {
			Refresh string `json:"refresh_token"`
			Expires int64  `json:"expires_in"`
			Scope   string `json:"scope"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &token)
		event["refresh_present"], event["expires_in"], event["granted_scope"], event["oauth_error"] = token.Refresh != "", token.Expires, token.Scope, token.Error
		p.mu.Lock()
		if token.Refresh != "" {
			event["refresh_rotated"] = p.previousRefresh != "" && p.previousRefresh != token.Refresh
			p.previousRefresh = token.Refresh
		}
		p.mu.Unlock()
	}
	if rpc.Method == "initialize" || rpc.Method == "tools/list" || rpc.Method == "tools/call" {
		var response struct {
			Result struct {
				Protocol string            `json:"protocolVersion"`
				Tools    []json.RawMessage `json:"tools"`
				IsError  bool              `json:"isError"`
			} `json:"result"`
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal(recorder.Body.Bytes(), &response)
		if rpc.Method == "initialize" {
			event["negotiated_protocol"] = response.Result.Protocol
		}
		if rpc.Method == "tools/list" {
			event["tools"] = response.Result.Tools
		}
		if rpc.Method == "tools/call" {
			event["tool_error"] = response.Result.IsError
			event["rpc_error"] = len(response.Error) > 0
		}
	}
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()
	for name, values := range recorder.Header() {
		w.Header()[name] = values
	}
	w.WriteHeader(recorder.Code)
	_, _ = w.Write(recorder.Body.Bytes())
}
