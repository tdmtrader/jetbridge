package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc/api/accessor"
	jetbridgemcp "github.com/concourse/concourse/atc/mcp"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type mcpTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

type mcpAuthorization struct{ Client, Redirect, Verifier, Code string }

type MCPScenario struct {
	Fixture                        *AuthFixture
	Config                         mcpauth.Config
	Server                         *mcpauth.Server
	Tokens, Previous, Other        mcpTokens
	ClientID                       string
	Status                         int
	OAuthError                     string
	ClockOffset                    atomic.Int64
	TargetTeam, APIToken, Boundary string
}

func newMCPScenario(f *AuthFixture) (*MCPScenario, error) {
	s := &MCPScenario{Fixture: f, ClientID: "client-a"}
	s.Config = mcpauth.Config{
		Issuer: f.URL + "/mcp/oauth", Resource: f.URL + "/api/v1/mcp", Store: mcpauth.NewSQLStore(f.MCPConn),
		Now: func() time.Time { return time.Now().Add(time.Duration(s.ClockOffset.Load())) },
		Clients: []mcpauth.Client{
			{ID: "client-a", Name: "Brine editor", RedirectURIs: []string{f.URL + "/mcp/client-a/callback"}},
			{ID: "client-b", Name: "Brine assistant", RedirectURIs: []string{f.URL + "/mcp/client-b/callback"}},
		},
		Provider: mcpauth.OAuthProvider{
			Config: &oauth2.Config{ClientID: "brine-mcp", ClientSecret: "brine-mcp-secret", RedirectURL: f.URL + "/mcp/oauth/callback",
				Endpoint: oauth2.Endpoint{AuthURL: f.URL + "/sky/issuer/auth", TokenURL: f.URL + "/sky/issuer/token"},
				Scopes:   []string{"openid", "profile", "email", "groups", "federated:id", "offline_access"}},
			HTTPClient:    f.Client,
			VerifyIDToken: mcpauth.NewOIDCVerifier(f.URL+"/sky/issuer", "brine-mcp", f.Client),
		},
	}
	if err := s.install(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *MCPScenario) install() error {
	server, err := mcpauth.NewServer(s.Config)
	if err != nil {
		return err
	}
	api, err := s.Fixture.apiHandler(accessor.NewTrustedTokenVerifier(s.Fixture.Verifier))
	if err != nil {
		return err
	}
	mcp := jetbridgemcp.NewHandler(server, api, jetbridgemcp.HandlerOptions{AccessFactory: accessor.NewAccessFactory(accessor.NewTrustedTokenVerifier(s.Fixture.Verifier), s.Fixture.DB.TeamFactory, "", nil, nil), CustomRoles: s.Fixture.CustomRoles})
	// This tiny boundary exercises scope enforcement for future mutation
	// adapters. It does not pretend pipeline-write or hijack MCP tools exist.
	probe := server.AuthorizeHTTP(func(w http.ResponseWriter, r *http.Request, p mcpauth.Principal) {
		if !server.RequireScope(w, p, r.Header.Get("X-Brine-Scope")) {
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	writePipeline := server.AuthorizeHTTP(func(w http.ResponseWriter, r *http.Request, p mcpauth.Principal) {
		if !server.RequireScope(w, p, mcpauth.ScopePipelines) {
			return
		}
		forward := r.Clone(accessor.WithTrustedClaims(r.Context(), p.Claims))
		forward.Header = r.Header.Clone()
		forward.Header.Del("Authorization")
		forward.URL.Path = "/api/v1/teams/auth-team/pipelines/private/config"
		api.ServeHTTP(w, forward)
	})
	s.Fixture.mu.Lock()
	s.Fixture.API = api
	s.Fixture.extra = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/mcp":
			mcp.ServeHTTP(w, r)
		case "/mcp/probe":
			probe.ServeHTTP(w, r)
		case "/mcp/pipeline-config":
			writePipeline.ServeHTTP(w, r)
		default:
			server.ServeHTTP(w, r)
		}
	})
	s.Fixture.mu.Unlock()
	s.Server = server
	return nil
}

func (s *MCPScenario) approve(clientID, requested, selected, user string) (mcpAuthorization, error) {
	request := mcpAuthorization{Client: clientID, Redirect: s.Fixture.URL + "/mcp/" + clientID + "/callback", Verifier: oauth2.GenerateVerifier()}
	q := url.Values{"client_id": {clientID}, "redirect_uri": {request.Redirect}, "response_type": {"code"},
		"resource": {s.Config.Resource}, "scope": {requested}, "state": {"brine-client-state"},
		"code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(request.Verifier)}}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return request, err
	}
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.String() == request.Redirect || strings.HasPrefix(req.URL.String(), request.Redirect+"?") {
			return http.ErrUseLastResponse
		}
		if len(via) >= 12 {
			return errors.New("too many authorization redirects")
		}
		return nil
	}}
	resp, err := s.Fixture.loginForm(client, s.Config.Issuer+"/authorize?"+q.Encode(), user)
	if err != nil {
		return request, err
	}
	body, err := authReadBody(resp)
	if err != nil {
		return request, err
	}
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/mcp/oauth/consent" {
		return request, fmt.Errorf("real issuer callback did not reach consent: HTTP %d at %s", resp.StatusCode, resp.Request.URL.Path)
	}
	action, form, err := authForm(body, resp.Request.URL)
	if err != nil {
		return request, fmt.Errorf("MCP consent form: %w", err)
	}
	form.Set("decision", "allow")
	for _, scope := range strings.Fields(selected) {
		form.Add("scope", scope)
	}
	resp, err = client.PostForm(action, form)
	if err != nil {
		return request, err
	}
	_, _ = authReadBody(resp)
	if resp.StatusCode != http.StatusSeeOther {
		return request, fmt.Errorf("consent did not return an authorization code: HTTP %d", resp.StatusCode)
	}
	redirect, err := resp.Location()
	if err != nil {
		return request, err
	}
	if redirect.Query().Get("state") != "brine-client-state" || redirect.Query().Get("iss") != s.Config.Issuer {
		return request, errors.New("authorization response lost client state or issuer")
	}
	request.Code = redirect.Query().Get("code")
	if request.Code == "" {
		return request, errors.New("consent response did not contain an authorization code")
	}
	return request, nil
}

func (s *MCPScenario) exchange(a mcpAuthorization, override string) (mcpTokens, int, error) {
	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {a.Client}, "resource": {s.Config.Resource},
		"code": {a.Code}, "code_verifier": {a.Verifier}, "redirect_uri": {a.Redirect}}
	switch override {
	case "client":
		form.Set("client_id", "client-b")
	case "resource":
		form.Set("resource", s.Fixture.URL+"/unrelated-api")
	case "verifier":
		form.Set("code_verifier", oauth2.GenerateVerifier())
	case "":
	default:
		return mcpTokens{}, 0, fmt.Errorf("unknown authorization binding %q", override)
	}
	return s.tokenRequest(form)
}

func (s *MCPScenario) tokenRequest(form url.Values) (mcpTokens, int, error) {
	resp, err := s.Fixture.Client.PostForm(s.Config.Issuer+"/token", form)
	if err != nil {
		return mcpTokens{}, 0, err
	}
	body, err := authReadBody(resp)
	if err != nil {
		return mcpTokens{}, resp.StatusCode, err
	}
	var tokens mcpTokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return tokens, resp.StatusCode, fmt.Errorf("token endpoint did not return JSON: %w", err)
	}
	return tokens, resp.StatusCode, nil
}

func (s *MCPScenario) authorize(client, requested, selected, user string) (mcpTokens, error) {
	a, err := s.approve(client, requested, selected, user)
	if err != nil {
		return mcpTokens{}, err
	}
	tokens, status, err := s.exchange(a, "")
	if err != nil {
		return tokens, err
	}
	if status != http.StatusOK || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		return tokens, fmt.Errorf("authorization code exchange failed: HTTP %d (%s)", status, tokens.Error)
	}
	return tokens, nil
}

func (s *MCPScenario) refresh(client, refresh, scope string) (mcpTokens, int, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {client}, "resource": {s.Config.Resource}, "refresh_token": {refresh}}
	if scope != "" {
		form.Set("scope", scope)
	}
	return s.tokenRequest(form)
}

type mcpBearerTransport struct{ Token string }

func (t mcpBearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.Token)
	return http.DefaultTransport.RoundTrip(copy)
}

func (s *MCPScenario) readPipeline(token string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "brine-auth-acceptance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: s.Config.Resource,
		HTTPClient:           &http.Client{Transport: mcpBearerTransport{Token: token}, Timeout: 10 * time.Second},
		DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return fmt.Errorf("official MCP client initialize: %w", err)
	}
	defer session.Close()
	list, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return fmt.Errorf("official MCP client tools/list: %w", err)
	}
	if !slices.ContainsFunc(list.Tools, func(t *sdk.Tool) bool { return t.Name == "pipeline_status" }) {
		return errors.New("tools/list did not advertise pipeline_status")
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "pipeline_status", Arguments: map[string]any{"team": "auth-team", "pipeline": "private"}})
	if err != nil {
		return fmt.Errorf("official MCP client pipeline_status: %w", err)
	}
	if result.IsError {
		return errors.New("pipeline_status returned a tool error")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if !strings.Contains(string(encoded), "private") {
		return errors.New("pipeline_status did not return the private pipeline")
	}
	return nil
}

func (s *MCPScenario) probe(token, scope string) (int, error) {
	req, err := http.NewRequest(http.MethodGet, s.Fixture.URL+"/mcp/probe", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Brine-Scope", scope)
	resp, err := s.Fixture.Client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = authReadBody(resp)
	return resp.StatusCode, nil
}

func MCPAuthenticationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[AuthScenario, *MCPScenario]("an MCP client requests {string} and the user selects {string}", func(in AuthScenario, p brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			requested, _ := p.GetString(0)
			selected, _ := p.GetString(1)
			s, err := newMCPScenario(in.Fixture)
			if err != nil {
				return nil, err
			}
			s.Tokens, err = s.authorize(s.ClientID, requested, selected, "owner")
			return s, err
		}),
		CheckThat[*MCPScenario]("the MCP client can read the private pipeline through the protocol", func(s *MCPScenario) error { return s.readPipeline(s.Tokens.AccessToken) }),
		check[*MCPScenario]("the MCP grant has exactly {string}", func(s *MCPScenario, p brine.Params) error {
			want, _ := p.GetString(0)
			actual, expected := strings.Fields(s.Tokens.Scope), strings.Fields(want)
			slices.Sort(actual)
			slices.Sort(expected)
			if !slices.Equal(actual, expected) {
				return fmt.Errorf("granted scopes %v, expected %v", actual, expected)
			}
			return nil
		}),
		check[*MCPScenario]("the MCP grant permits pipeline changes {string} and hijack {string}", func(s *MCPScenario, p brine.Params) error {
			for i, scope := range []string{mcpauth.ScopePipelines, mcpauth.ScopeHijack} {
				allowed, _ := p.GetString(i)
				want := http.StatusForbidden
				if allowed == "yes" {
					want = http.StatusOK
				}
				status, err := s.probe(s.Tokens.AccessToken, scope)
				if err != nil {
					return err
				}
				if status != want {
					return fmt.Errorf("scope %s returned HTTP %d, expected %d", scope, status, want)
				}
			}
			return nil
		}),
		brine.DefineMap[AuthScenario, *MCPScenario]("a viewer authorizes an MCP client with pipeline write permission", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			s, err := newMCPScenario(in.Fixture)
			if err != nil {
				return nil, err
			}
			s.Tokens, err = s.authorize(s.ClientID, "read pipelines:write", "read pipelines:write", "viewer")
			return s, err
		}),
		CheckThat[*MCPScenario]("MCP denies changing the private pipeline", func(s *MCPScenario) error {
			req, err := http.NewRequest(http.MethodPut, s.Fixture.URL+"/mcp/pipeline-config", strings.NewReader("jobs: []\n"))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+s.Tokens.AccessToken)
			req.Header.Set("Content-Type", "application/x-yaml")
			resp, err := s.Fixture.Client.Do(req)
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusForbidden {
				return fmt.Errorf("viewer with pipeline scope received HTTP %d, expected 403", resp.StatusCode)
			}
			return nil
		}),
		brine.DefineMap[AuthScenario, *MCPScenario]("an approved MCP authorization code is redeemed with the wrong {string}", func(in AuthScenario, p brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			binding, _ := p.GetString(0)
			s, err := newMCPScenario(in.Fixture)
			if err != nil {
				return nil, err
			}
			a, err := s.approve(s.ClientID, "read", "read", "owner")
			if err != nil {
				return s, err
			}
			tokens, status, err := s.exchange(a, binding)
			s.Status, s.OAuthError = status, tokens.Error
			return s, err
		}),
		CheckThat[*MCPScenario]("the MCP token exchange is rejected", func(s *MCPScenario) error {
			if s.Status != http.StatusBadRequest || s.OAuthError == "" {
				return fmt.Errorf("expected OAuth rejection, got HTTP %d (%s)", s.Status, s.OAuthError)
			}
			return nil
		}),
		brine.DefineMap[*MCPScenario, *MCPScenario]("the MCP client tries to refresh with pipeline write permission", func(s *MCPScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			tokens, status, err := s.refresh(s.ClientID, s.Tokens.RefreshToken, "read pipelines:write")
			s.Status, s.OAuthError = status, tokens.Error
			return s, err
		}),
		brine.DefineMap[*MCPScenario, *MCPScenario]("the MCP authorization server restarts with the same database", func(s *MCPScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			// A new Store and Server share only persisted PostgreSQL state.
			s.Config.Store = mcpauth.NewSQLStore(s.Fixture.MCPConn)
			return s, s.install()
		}),
		CheckThat[*MCPScenario]("MCP authorization state is encrypted in PostgreSQL", func(s *MCPScenario) error {
			rows, err := s.Fixture.DB.Conn.Query("SELECT data, nonce FROM mcp_oauth_state")
			if err != nil {
				return err
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var data string
				var nonce *string
				if err := rows.Scan(&data, &nonce); err != nil {
					return err
				}
				if nonce == nil || json.Valid([]byte(data)) || strings.Contains(data, s.Tokens.RefreshToken) || strings.Contains(data, s.Tokens.AccessToken) {
					return errors.New("MCP authorization state was stored without encryption")
				}
				plain, err := s.Fixture.MCPConn.EncryptionStrategy().Decrypt(data, nonce)
				if err != nil {
					return fmt.Errorf("decrypt persisted MCP state: %w", err)
				}
				if !json.Valid(plain) {
					return errors.New("decrypted MCP state is not valid JSON")
				}
				count++
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if count == 0 {
				return errors.New("no persisted MCP authorization records were checked")
			}
			return nil
		}),
		brine.DefineMap[*MCPScenario, *MCPScenario]("the MCP client renews its grant", func(s *MCPScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			s.Previous = s.Tokens
			tokens, status, err := s.refresh(s.ClientID, s.Tokens.RefreshToken, "")
			if err != nil {
				return s, err
			}
			if status != http.StatusOK || tokens.AccessToken == "" || tokens.RefreshToken == s.Previous.RefreshToken {
				return s, fmt.Errorf("refresh did not issue replacement credentials: HTTP %d (%s)", status, tokens.Error)
			}
			s.Tokens = tokens
			return s, nil
		}),
		brine.DefineMap[*MCPScenario, *MCPScenario]("the MCP client replays the previous refresh credential", func(s *MCPScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			tokens, status, err := s.refresh(s.ClientID, s.Previous.RefreshToken, "")
			s.Status, s.OAuthError = status, tokens.Error
			return s, err
		}),
		CheckThat[*MCPScenario]("the replacement MCP access credential no longer authorizes requests", func(s *MCPScenario) error {
			status, err := s.probe(s.Tokens.AccessToken, mcpauth.ScopeRead)
			if err != nil {
				return err
			}
			if status != http.StatusUnauthorized {
				return fmt.Errorf("replacement access credential survived replay: HTTP %d", status)
			}
			return nil
		}),
		brine.DefineMap[AuthScenario, *MCPScenario]("two MCP clients receive independent read grants", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			s, err := newMCPScenario(in.Fixture)
			if err != nil {
				return nil, err
			}
			s.Tokens, err = s.authorize("client-a", "read", "read", "owner")
			if err != nil {
				return s, err
			}
			s.Other, err = s.authorize("client-b", "read", "read", "owner")
			return s, err
		}),
		brine.DefineMap[*MCPScenario, *MCPScenario]("the first MCP client revokes its grant", func(s *MCPScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			resp, err := s.Fixture.Client.PostForm(s.Config.Issuer+"/revoke", url.Values{"client_id": {s.ClientID}, "token": {s.Tokens.RefreshToken}})
			if err != nil {
				return s, err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusOK {
				return s, fmt.Errorf("revocation returned HTTP %d", resp.StatusCode)
			}
			return s, nil
		}),
		CheckThat[*MCPScenario]("the first MCP grant can neither read nor renew", func(s *MCPScenario) error {
			status, err := s.probe(s.Tokens.AccessToken, mcpauth.ScopeRead)
			if err != nil {
				return err
			}
			if status != http.StatusUnauthorized {
				return fmt.Errorf("revoked grant still authorizes: HTTP %d", status)
			}
			_, status, err = s.refresh(s.ClientID, s.Tokens.RefreshToken, "")
			if err != nil {
				return err
			}
			if status != http.StatusBadRequest {
				return fmt.Errorf("revoked grant can still renew: HTTP %d", status)
			}
			return nil
		}),
		CheckThat[*MCPScenario]("the second MCP client can still read the private pipeline", func(s *MCPScenario) error {
			tokens, status, err := s.refresh("client-b", s.Other.RefreshToken, "")
			if err != nil {
				return err
			}
			if status != http.StatusOK {
				return fmt.Errorf("independent grant could not renew: HTTP %d (%s)", status, tokens.Error)
			}
			return s.readPipeline(tokens.AccessToken)
		}),
	}
}
