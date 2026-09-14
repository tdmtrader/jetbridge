package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/internal/mcpclient"
	"github.com/concourse/concourse/skymarshal/mcpauth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

type mcpReferenceScenario struct {
	MCP           *MCPScenario
	Config        mcpclient.Config
	Client        *mcpclient.Client
	Before, After oauth2.Token
}

func (s *mcpReferenceScenario) login() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.Client.Login(ctx, func(start string) error {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return err
		}
		browser := &http.Client{Jar: jar, Timeout: 10 * time.Second}
		resp, err := s.MCP.Fixture.loginForm(browser, start, "owner")
		if err != nil {
			return err
		}
		body, err := authReadBody(resp)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/mcp/oauth/consent" {
			return fmt.Errorf("reference client login did not reach consent: HTTP %d", resp.StatusCode)
		}
		action, form, err := authForm(body, resp.Request.URL)
		if err != nil {
			return err
		}
		form.Set("decision", "allow")
		form.Add("scope", "read")
		resp, err = browser.PostForm(action, form)
		if err != nil {
			return err
		}
		_, _ = authReadBody(resp)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("reference client callback: HTTP %d", resp.StatusCode)
		}
		return nil
	})
}

func (s *mcpReferenceScenario) savedToken() (oauth2.Token, error) {
	var state struct {
		Token oauth2.Token `json:"token"`
	}
	body, err := os.ReadFile(s.Config.StatePath)
	if err != nil {
		return state.Token, err
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return state.Token, err
	}
	return state.Token, nil
}

func MCPReferenceDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[AuthScenario, *mcpReferenceScenario]("the reference MCP client logs in through discovery and browser consent", func(in AuthScenario, _ brine.Params, _ *brine.Recorder) (*mcpReferenceScenario, error) {
			mcp, err := newMCPScenario(in.Fixture)
			if err != nil {
				return nil, err
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				return nil, err
			}
			redirect := "http://" + listener.Addr().String() + "/callback"
			if err := listener.Close(); err != nil {
				return nil, err
			}
			mcp.Config.Clients = append(mcp.Config.Clients, mcpauth.Client{ID: "brine-reference", Name: "Brine reference client", RedirectURIs: []string{redirect}})
			if err := mcp.install(); err != nil {
				return nil, err
			}
			s := &mcpReferenceScenario{MCP: mcp, Config: mcpclient.Config{Endpoint: mcp.Config.Resource, ClientID: "brine-reference", RedirectURL: redirect,
				StatePath: filepath.Join(in.Fixture.Home, "mcp.json"), Scopes: []string{"read", "offline_access"}, HTTPClient: in.Fixture.Client}}
			s.Client, err = mcpclient.New(s.Config)
			if err != nil {
				return s, err
			}
			if err := s.login(); err != nil {
				return s, err
			}
			s.Before, err = s.savedToken()
			return s, err
		}),
		brine.DefineMap[*mcpReferenceScenario, *mcpReferenceScenario]("the reference client and authorization server restart with their saved state", func(s *mcpReferenceScenario, _ brine.Params, _ *brine.Recorder) (*mcpReferenceScenario, error) {
			s.MCP.Config.Store = mcpauth.NewSQLStore(s.MCP.Fixture.MCPConn)
			if err := s.MCP.install(); err != nil {
				return s, err
			}
			var err error
			s.Client, err = mcpclient.New(s.Config)
			return s, err
		}),
		brine.DefineMap[*mcpReferenceScenario, *mcpReferenceScenario]("the reference client's access credential expires", func(s *mcpReferenceScenario, _ brine.Params, _ *brine.Recorder) (*mcpReferenceScenario, error) {
			s.MCP.ClockOffset.Store(int64(16 * time.Minute))
			status, err := s.MCP.probe(s.Before.AccessToken, mcpauth.ScopeRead)
			if err != nil {
				return s, err
			}
			if status != http.StatusUnauthorized {
				return s, fmt.Errorf("original access credential did not expire: HTTP %d", status)
			}
			return s, nil
		}),
		CheckThat[*mcpReferenceScenario]("the reference MCP client reads the private pipeline automatically", func(s *mcpReferenceScenario) error {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			session, err := s.Client.Connect(ctx)
			if err != nil {
				return fmt.Errorf("reference MCP initialize/automatic refresh: %w", err)
			}
			defer session.Close()
			list, err := session.ListTools(ctx, &sdk.ListToolsParams{})
			if err != nil {
				return err
			}
			if !slices.ContainsFunc(list.Tools, func(tool *sdk.Tool) bool { return tool.Name == "pipeline_status" }) {
				return errors.New("reference client did not discover pipeline_status")
			}
			result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "pipeline_status", Arguments: map[string]any{"team": "auth-team", "pipeline": "private"}})
			if err != nil {
				return err
			}
			if result.IsError {
				return errors.New("reference pipeline_status returned a tool error")
			}
			body, err := json.Marshal(result.StructuredContent)
			if err != nil {
				return err
			}
			var status struct {
				Pipeline string `json:"pipeline"`
			}
			if err := json.Unmarshal(body, &status); err != nil {
				return err
			}
			if status.Pipeline != "private" {
				return errors.New("reference pipeline_status did not return the private pipeline")
			}
			return nil
		}),
		brine.DefineMap[*mcpReferenceScenario, *mcpReferenceScenario]("the reference client has saved the rotated refresh credential", func(s *mcpReferenceScenario, _ brine.Params, _ *brine.Recorder) (*mcpReferenceScenario, error) {
			var err error
			s.After, err = s.savedToken()
			if err != nil {
				return s, err
			}
			if s.After.RefreshToken == "" || s.After.RefreshToken == s.Before.RefreshToken || s.After.AccessToken == s.Before.AccessToken {
				return s, errors.New("reference client did not persist both replacement credentials")
			}
			info, err := os.Stat(s.Config.StatePath)
			if err != nil {
				return s, err
			}
			if info.Mode().Perm() != 0600 {
				return s, fmt.Errorf("reference credential mode is %o, expected0600", info.Mode().Perm())
			}
			return s, nil
		}),
		brine.DefineMap[*mcpReferenceScenario, *mcpReferenceScenario]("the reference client logs out", func(s *mcpReferenceScenario, _ brine.Params, _ *brine.Recorder) (*mcpReferenceScenario, error) {
			return s, s.Client.Logout(context.Background())
		}),
		CheckThat[*mcpReferenceScenario]("its saved grant is revoked and local credentials are removed", func(s *mcpReferenceScenario) error {
			if _, err := os.Stat(s.Config.StatePath); !errors.Is(err, os.ErrNotExist) {
				return errors.New("reference credential file was not removed")
			}
			status, err := s.MCP.probe(s.After.AccessToken, mcpauth.ScopeRead)
			if err != nil {
				return err
			}
			if status != http.StatusUnauthorized {
				return errors.New("reference access credential survived logout")
			}
			_, status, err = s.MCP.refresh(s.Config.ClientID, s.After.RefreshToken, "")
			if err != nil {
				return err
			}
			if status != http.StatusBadRequest {
				return errors.New("reference refresh credential survived logout")
			}
			return nil
		}),
		brine.DefineMap[*mcpReferenceScenario, *mcpReferenceScenario]("the reference MCP client logs in again", func(s *mcpReferenceScenario, _ brine.Params, _ *brine.Recorder) (*mcpReferenceScenario, error) {
			return s, s.login()
		}),
	}
}
