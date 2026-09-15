package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/brine-dev/brine-go/pkg/brine"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/api/accessor"
	jetbridgemcp "github.com/concourse/concourse/atc/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func MCPOperationDefinitions() []brine.StepDefinition {
	return []brine.StepDefinition{
		CheckThat[*MCPScenario]("precise MCP completes the pipeline and build workflow", (*MCPScenario).preciseWorkflow),
		CheckThat[*MCPScenario]("precise MCP preserves independent and changing authorities", (*MCPScenario).preciseAuthorities),
		CheckThat[*MCPScenario]("precise MCP distinguishes missing consent from unsupported and malformed calls", (*MCPScenario).preciseDiagnostics),
	}
}
func (s *MCPScenario) preciseSession(ctx context.Context) (*sdk.ClientSession, error) {
	return sdk.NewClient(&sdk.Implementation{Name: "brine-precise", Version: "1"}, nil).Connect(ctx, &sdk.StreamableClientTransport{Endpoint: s.Config.Resource, HTTPClient: &http.Client{Transport: mcpBearerTransport{Token: s.Tokens.AccessToken}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
}
func preciseCall(ctx context.Context, session *sdk.ClientSession, resource, op string, args map[string]any) (map[string]any, error) {
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: resource, Arguments: map[string]any{"request": map[string]any{"operation": op, "arguments": args}}})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		b, _ := json.Marshal(result.Content)
		return nil, fmt.Errorf("%s: %s", op, b)
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > 1024*1024 {
		return nil, errors.New("MCP result exceeded total byte bound")
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, err
	}
	var v struct {
		Operation string         `json:"operation"`
		Result    map[string]any `json:"result"`
	}
	err = json.Unmarshal(b, &v)
	if err == nil && v.Operation != op {
		err = errors.New("wrong result operation")
	}
	return v.Result, err
}
func preciseTarget(name string, vars atc.InstanceVars) map[string]any {
	return map[string]any{"team": "auth-team", "pipeline": name, "instance_vars": vars}
}
func (s *MCPScenario) preciseWorkflow() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	session, err := s.preciseSession(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	vars := atc.InstanceVars{"branch": "feature"}
	target := preciseTarget("precise", vars)
	config := "jobs:\n- name: unit\n  plan: []\n"
	target["config_yaml"], target["version"] = config, "0"
	receipt, err := preciseCall(ctx, session, "pipeline", "pipeline_config_set", target)
	if err != nil {
		return err
	}
	if receipt["created"] != true {
		return errors.New("create receipt missing")
	}
	if _, err = preciseCall(ctx, session, "pipeline", "pipeline_config_set", target); err == nil || !strings.Contains(err.Error(), "VERSION_CONFLICT") {
		return fmt.Errorf("duplicate create: %v", err)
	}
	target = preciseTarget("precise", vars)
	got, err := preciseCall(ctx, session, "pipeline", "pipeline_config_get", target)
	if err != nil {
		return err
	}
	if got["version"] != receipt["version"] || !strings.Contains(got["config_yaml"].(string), "unit") {
		return errors.New("config receipt/read mismatch")
	}
	target["version"], target["config_yaml"] = got["version"], got["config_yaml"]
	if _, err = preciseCall(ctx, session, "pipeline", "pipeline_config_set", target); err != nil {
		return err
	}
	target = preciseTarget("precise", vars)
	for _, op := range []string{"pipeline_unpause", "pipeline_pause"} {
		if _, err = preciseCall(ctx, session, "pipeline", op, target); err != nil {
			return err
		}
	}
	status, err := preciseCall(ctx, session, "pipeline", "pipeline_get", target)
	if err != nil {
		return err
	}
	if status["paused"] != true || status["instance_vars"].(map[string]any)["branch"] != "feature" {
		return errors.New("instance/state mismatch")
	}
	if _, err = preciseCall(ctx, session, "pipeline", "pipeline_get", preciseTarget("precise", atc.InstanceVars{})); err == nil {
		return errors.New("non-instance accidentally selected instance")
	}
	page, err := preciseCall(ctx, session, "pipeline", "pipelines_list", map[string]any{"team": "auth-team", "query": "precise", "limit": 1})
	if err != nil {
		return err
	}
	if len(page["items"].([]any)) != 1 {
		return errors.New("pipeline discovery did not find exact instance")
	}
	target["job"] = "unit"
	receipt, err = preciseCall(ctx, session, "job", "job_trigger", target)
	if err != nil {
		return err
	}
	id := int(receipt["build_id"].(float64))
	build, found, err := s.Fixture.DB.BuildFactory.Build(id)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("trigger did not create build")
	}
	if build.PipelineID() != int(status["id"].(float64)) {
		return errors.New("trigger selected wrong instance")
	}
	table := fmt.Sprintf("pipeline_build_events_%d", build.PipelineID())
	log := strings.Repeat("<\x00🙂", 12000)
	payload, _ := json.Marshal(map[string]any{"payload": log, "origin": map[string]any{"id": "unit", "source": "stdout"}})
	if _, err = s.Fixture.DB.Conn.Exec("INSERT INTO "+table+" (event_id,build_id,type,version,payload) VALUES (5,$1,'log','5.0',$2)", id, string(payload)); err != nil {
		return err
	}
	combined := ""
	cursor := ""
	for i := 0; i < 30; i++ {
		a := map[string]any{"build_id": id, "max_bytes": 8192}
		if cursor != "" {
			a["cursor"] = cursor
		}
		p, err := preciseCall(ctx, session, "build", "build_logs_read", a)
		if err != nil {
			return err
		}
		for _, row := range p["events"].([]any) {
			e := row.(map[string]any)
			if e["event"] == "log" {
				combined += e["data"].(map[string]any)["payload"].(string)
			}
		}
		cursor = p["next_cursor"].(string)
		if p["caught_up"] == true {
			break
		}
	}
	if combined != log {
		return errors.New("MCP log reconstruction lost output")
	}
	if _, err = preciseCall(ctx, session, "build", "build_get", map[string]any{"build_id": id}); err != nil {
		return err
	}
	if _, err = preciseCall(ctx, session, "build", "build_abort", map[string]any{"build_id": id}); err != nil {
		return err
	}
	if _, err = build.Reload(); err != nil {
		return err
	}
	if !build.IsAborted() {
		return errors.New("abort receipt had no effect")
	}
	page, err = preciseCall(ctx, session, "build", "builds_list", target)
	if err != nil {
		return err
	}
	if len(page["items"].([]any)) != 1 {
		return errors.New("trigger created duplicate builds")
	}
	return nil
}
func (s *MCPScenario) preciseAuthorities() error {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// A write-only grant can apply supplied configuration without a hidden read.
	tokens, err := s.authorize("client-b", "pipelines:write", "pipelines:write", "owner")
	if err != nil {
		return err
	}
	s.Tokens = tokens
	session, err := s.preciseSession(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	list, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return err
	}
	b, _ := json.Marshal(list)
	if strings.Contains(string(b), `"const":"pipeline_config_get"`) || strings.Contains(string(b), `"name":"build"`) {
		return errors.New("write-only catalog leaked read branches")
	}
	target := preciseTarget("write-only", atc.InstanceVars{})
	target["version"], target["config_yaml"] = "0", "jobs:\n- name: unit\n  plan: []\n"
	if _, err = preciseCall(ctx, session, "pipeline", "pipeline_config_set", target); err != nil {
		return err
	}
	// Exact custom actions remain independent inside one resource tool.
	s.Fixture.CustomRoles = map[string]string{atc.PausePipeline: accessor.ViewerRole, atc.UnpausePipeline: accessor.OwnerRole}
	if err = s.install(); err != nil {
		return err
	}
	s.Tokens, err = s.authorize("client-a", "read pipelines:write", "read pipelines:write", "viewer")
	if err != nil {
		return err
	}
	viewer, err := s.preciseSession(ctx)
	if err != nil {
		return err
	}
	defer viewer.Close()
	list, err = viewer.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return err
	}
	b, _ = json.Marshal(list)
	if !strings.Contains(string(b), `"const":"pipeline_pause"`) || strings.Contains(string(b), `"const":"pipeline_unpause"`) {
		return errors.New("custom action catalog not pruned independently")
	}
	target = preciseTarget("private", atc.InstanceVars{})
	if _, err = preciseCall(ctx, viewer, "pipeline", "pipeline_pause", target); err != nil {
		return err
	}
	if _, err = preciseCall(ctx, viewer, "pipeline", "pipeline_unpause", target); err == nil {
		return errors.New("viewer bypassed unpause authority")
	}
	// Public build metadata must not expose a private job's output.
	foreign, err := s.Fixture.DB.TeamFactory.CreateTeam(atc.Team{Name: "foreign", Auth: atc.TeamAuth{"owner": {"users": {"local:someone-else"}}}})
	if err != nil {
		return err
	}
	public, _, err := foreign.SavePipeline(atc.PipelineRef{Name: "public"}, atc.Config{Jobs: atc.JobConfigs{{Name: "hidden", Public: false}}}, 0, true)
	if err != nil {
		return err
	}
	if err = public.Expose(); err != nil {
		return err
	}
	job, found, err := public.Job("hidden")
	if err != nil {
		return err
	}
	if !found {
		return errors.New("missing private job")
	}
	build, err := job.CreateBuild("fixture")
	if err != nil {
		return err
	}
	if _, err = preciseCall(ctx, viewer, "build", "build_get", map[string]any{"build_id": build.ID()}); err != nil {
		return err
	}
	if _, err = preciseCall(ctx, viewer, "build", "build_logs_read", map[string]any{"build_id": build.ID()}); err == nil {
		return errors.New("public metadata leaked private job output")
	}
	// The same session must consult changed authority after listing.
	s.Fixture.CustomRoles[atc.PausePipeline] = accessor.OwnerRole
	if err = s.install(); err != nil {
		return err
	}
	if _, err = preciseCall(ctx, viewer, "pipeline", "pipeline_pause", target); err == nil {
		return errors.New("stale listed branch bypassed changed role")
	}
	// Revocation after listing: the listed catalog confers nothing.
	resp, err := s.Fixture.Client.PostForm(s.Config.Issuer+"/revoke", url.Values{"client_id": {"client-a"}, "token": {s.Tokens.RefreshToken}})
	if err != nil {
		return err
	}
	_, _ = authReadBody(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("revocation returned HTTP %d", resp.StatusCode)
	}
	if _, err = preciseCall(ctx, viewer, "pipeline", "pipeline_get", target); err == nil {
		return errors.New("revoked grant still executed a listed branch")
	}
	return nil
}
func (s *MCPScenario) preciseDiagnostics() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := s.preciseSession(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	list, err := session.ListTools(ctx, &sdk.ListToolsParams{})
	if err != nil {
		return err
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "capabilities_explain" {
		return errors.New("admin-only grant advertised executable branches")
	}
	for _, test := range []struct{ resource, operation, want string }{{"pipeline", "pipeline_config_set", "request_new_consent"}, {"container", "container_exec", "not_implemented"}, {"server", "unknown_action", "unknown"}} {
		result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "capabilities_explain", Arguments: map[string]any{"resource": test.resource, "operation": test.operation}})
		if err != nil {
			return err
		}
		b, _ := json.Marshal(result)
		if result.IsError || !strings.Contains(string(b), test.want) {
			return fmt.Errorf("diagnostic %s: %s", test.operation, b)
		}
	}
	for _, test := range []struct {
		resource, op, args string
		status             int
		want               string
	}{
		{"pipeline", "pipeline_config_set", `{"team":"auth-team","pipeline":"private","instance_vars":{},"version":"0","config_yaml":"jobs: []"}`, 403, "insufficient_scope"},
		{"pipeline", "pipeline_config_set", `{"team":"auth-team","pipeline":"private","instance_vars":{},"version":"0"}`, 200, "INVALID_ARGUMENTS"},
		{"build", "pipeline_pause", `{}`, 200, "INVALID_ARGUMENTS"},
		{"container", "container_exec", `{}`, 200, "NOT_IMPLEMENTED"},
	} {
		body := fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":%q,"arguments":{"request":{"operation":%q,"arguments":%s}}}}`, test.resource, test.op, test.args)
		resp, err := s.mcpRequest(s.Tokens.AccessToken, body)
		if err != nil {
			return err
		}
		b, err := authReadBody(resp)
		if err != nil {
			return err
		}
		if resp.StatusCode != test.status || !strings.Contains(string(b)+resp.Header.Get("WWW-Authenticate"), test.want) {
			return fmt.Errorf("%s HTTP %d: %s", test.op, resp.StatusCode, b)
		}
	}
	// A disabled alias must not bypass its canonical operation restriction.
	api := s.Fixture.API
	h := jetbridgemcp.NewHandler(s.Server, api, jetbridgemcp.HandlerOptions{AccessFactory: accessor.NewAccessFactory(accessor.NewTrustedTokenVerifier(s.Fixture.Verifier), s.Fixture.DB.TeamFactory, "", nil, nil), DisabledOperations: map[string]bool{"pipeline_get": true}})
	s.Fixture.mu.Lock()
	previous := s.Fixture.extra
	s.Fixture.extra = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/mcp" {
			h.ServeHTTP(w, r)
		} else {
			previous.ServeHTTP(w, r)
		}
	})
	s.Fixture.mu.Unlock()
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "pipeline_status", Arguments: map[string]any{"team": "auth-team", "pipeline": "private"}})
	if err != nil {
		return err
	}
	b, _ := json.Marshal(result)
	if !result.IsError || !strings.Contains(string(b), "DISABLED") {
		return errors.New("disabled alias not handled safely")
	}
	// An unrelated access-factory outage must not turn unknown/unsupported
	// product metadata into a permission request or make it disappear.
	unavailable := jetbridgemcp.NewHandler(s.Server, api, jetbridgemcp.HandlerOptions{})
	s.Fixture.mu.Lock()
	s.Fixture.extra = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/mcp" {
			unavailable.ServeHTTP(w, r)
		} else {
			previous.ServeHTTP(w, r)
		}
	})
	s.Fixture.mu.Unlock()
	for _, op := range []string{"container_exec", "unknown_action"} {
		r, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "capabilities_explain", Arguments: map[string]any{"resource": "container", "operation": op}})
		if err != nil {
			return err
		}
		if r.IsError {
			return errors.New("metadata blocked by unrelated access outage")
		}
	}
	r, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "capabilities_explain", Arguments: map[string]any{"resource": "pipeline", "operation": "pipeline_pause"}})
	if err != nil {
		return err
	}
	b, _ = json.Marshal(r)
	if !r.IsError || !strings.Contains(string(b), "TEMPORARY_ACCESS_FAILURE") {
		return errors.New("access outage misclassified as permission or impossibility")
	}
	return nil
}
