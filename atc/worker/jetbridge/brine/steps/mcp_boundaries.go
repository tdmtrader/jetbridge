package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	jetbridgemcp "github.com/concourse/concourse/atc/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (s *MCPScenario) apiRequest(team string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, s.Fixture.URL+"/api/v1/teams/"+team+"/pipelines/private", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIToken)
	return s.Fixture.Client.Do(req)
}

func (s *MCPScenario) flyIdentity(user string) error {
	if _, err := s.Fixture.fly("login", "-c", s.Fixture.URL, "-n", "auth-team", "-u", user, "-p", authPassword); err != nil {
		return err
	}
	token, err := s.Fixture.savedFlyToken()
	if err != nil {
		return err
	}
	s.APIToken = token.Value
	return nil
}

func (s *MCPScenario) callPipeline(team string) (*sdk.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := sdk.NewClient(&sdk.Implementation{Name: "brine-auth-boundary", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdk.StreamableClientTransport{
		Endpoint: s.Config.Resource, HTTPClient: &http.Client{Transport: mcpBearerTransport{Token: s.Tokens.AccessToken}},
		DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, err
	}
	defer session.Close()
	return session.CallTool(ctx, &sdk.CallToolParams{Name: "pipeline_status", Arguments: map[string]any{"team": team, "pipeline": "private"}})
}

func MCPBoundaryDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		brine.DefineMap[AuthScenario, *MCPScenario]("a read grant encounters a {string} restriction", func(in AuthScenario, p brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			boundary, _ := p.GetString(0)
			s, err := newMCPScenario(in.Fixture)
			if err != nil {
				return nil, err
			}
			s.Boundary, s.TargetTeam = boundary, "auth-team"
			user := "owner"
			switch boundary {
			case "foreign team":
				team, err := s.Fixture.DB.TeamFactory.CreateTeam(atc.Team{Name: "other-team", Auth: atc.TeamAuth{"owner": {"users": {"local:other"}}}})
				if err != nil {
					return s, err
				}
				if _, _, err := team.SavePipeline(atc.PipelineRef{Name: "private"}, atc.Config{Jobs: atc.JobConfigs{}}, 0, false); err != nil {
					return s, err
				}
				s.TargetTeam = "other-team"
			case "custom role":
				user = "viewer"
				s.Fixture.CustomRoles = map[string]string{atc.GetPipeline: accessor.OwnerRole}
			default:
				return s, fmt.Errorf("unknown authorization boundary %q", boundary)
			}
			if err := s.install(); err != nil {
				return s, err
			}
			s.Tokens, err = s.authorize(s.ClientID, "read", "read", user)
			if err != nil {
				return s, err
			}
			return s, s.flyIdentity(user)
		}),
		CheckThat[*MCPScenario]("both the API and MCP deny reading that pipeline", func(s *MCPScenario) error {
			response, err := s.apiRequest(s.TargetTeam)
			if err != nil {
				return err
			}
			_, _ = authReadBody(response)
			if response.StatusCode != http.StatusForbidden {
				return fmt.Errorf("%s API boundary returned HTTP %d", s.Boundary, response.StatusCode)
			}
			result, err := s.callPipeline(s.TargetTeam)
			if err != nil {
				return err
			}
			if !result.IsError {
				return fmt.Errorf("MCP bypassed the %s restriction", s.Boundary)
			}
			return nil
		}),
		CheckThat[*MCPScenario]("MCP pipeline status matches the authenticated API response", func(s *MCPScenario) error {
			if err := s.flyIdentity("owner"); err != nil {
				return err
			}
			response, err := s.apiRequest("auth-team")
			if err != nil {
				return err
			}
			body, err := authReadBody(response)
			if err != nil {
				return err
			}
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("API pipeline read: HTTP %d", response.StatusCode)
			}
			var pipeline atc.Pipeline
			if err := json.Unmarshal(body, &pipeline); err != nil {
				return err
			}
			result, err := s.callPipeline("auth-team")
			if err != nil {
				return err
			}
			if result.IsError {
				return errors.New("MCP pipeline read returned a tool error")
			}
			body, err = json.Marshal(result.StructuredContent)
			if err != nil {
				return err
			}
			var actual jetbridgemcp.PipelineStatus
			if err := json.Unmarshal(body, &actual); err != nil {
				return err
			}
			want := jetbridgemcp.PipelineStatus{ID: pipeline.ID, Team: pipeline.TeamName, Pipeline: pipeline.Name, Paused: pipeline.Paused, Archived: pipeline.Archived, Public: pipeline.Public}
			if actual != want {
				return fmt.Errorf("MCP status differs from API: got %+v, want %+v", actual, want)
			}
			return nil
		}),
		CheckThat[*MCPScenario]("a legacy fly bearer cannot initialize MCP", func(s *MCPScenario) error {
			if err := s.flyIdentity("owner"); err != nil {
				return err
			}
			resp, err := s.mcpRequest(s.APIToken, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"brine","version":"1"}}}`)
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusUnauthorized {
				return fmt.Errorf("legacy fly bearer MCP initialize: HTTP %d", resp.StatusCode)
			}
			return nil
		}),
		brine.DefineMap[*MCPScenario, *MCPScenario]("the MCP access credential expires", func(s *MCPScenario, _ brine.Params, _ *brine.Recorder) (*MCPScenario, error) {
			s.ClockOffset.Store(int64(16 * time.Minute))
			return s, nil
		}),
		CheckThat[*MCPScenario]("the expired MCP access credential is rejected", func(s *MCPScenario) error {
			resp, err := s.mcpRequest(s.Tokens.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "invalid_token") {
				return fmt.Errorf("expired MCP access: HTTP %d without expected challenge", resp.StatusCode)
			}
			return nil
		}),
		CheckThat[*MCPScenario]("calling pipeline status returns an insufficient read scope challenge", func(s *MCPScenario) error {
			resp, err := s.mcpRequest(s.Tokens.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"pipeline_status","arguments":{"team":"auth-team","pipeline":"private"}}}`)
			if err != nil {
				return err
			}
			_, _ = authReadBody(resp)
			challenge := resp.Header.Get("WWW-Authenticate")
			if resp.StatusCode != http.StatusForbidden || !strings.Contains(challenge, `error="insufficient_scope"`) || !strings.Contains(challenge, `scope="read"`) {
				return fmt.Errorf("missing read scope: HTTP %d without expected challenge", resp.StatusCode)
			}
			return nil
		}),
	}
}

func (s *MCPScenario) mcpRequest(token, body string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodPost, s.Config.Resource, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	return s.Fixture.Client.Do(req)
}
