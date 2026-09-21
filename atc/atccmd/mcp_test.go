package atccmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/skymarshal/token"
)

func TestMCPDoesNotConsumeOrClearWebLogin(t *testing.T) {
	var mcpAuthorization, apiAuthorization string
	cmd := &RunCommand{mcpHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mcpAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	})}
	other := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiAuthorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	})
	handler := cmd.constructHTTPHandler(lager.NewLogger("mcp-mount-test"), other, other, other, other, other, token.NewMiddleware(false))
	for _, path := range []string{"/api/v1/mcp", "/mcp/oauth/grants", "/.well-known/oauth-protected-resource/api/v1/mcp"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "skymarshal_auth", Value: "browser-login"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if mcpAuthorization != "" || len(w.Result().Cookies()) != 0 || w.Code != http.StatusUnauthorized {
			t.Fatalf("MCP route %s consumed/cleared the unrelated browser login: code=%d cookies=%d", path, w.Code, len(w.Result().Cookies()))
		}
	}
	for _, path := range []string{"/api/v1/user", "/api/v2/teams/main/pipelines/template/runs"} {
		apiAuthorization = ""
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.AddCookie(&http.Cookie{Name: "skymarshal_auth", Value: "browser-login"})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if apiAuthorization != "browser-login" || len(w.Result().Cookies()) == 0 {
			t.Fatalf("API cookie authentication no longer works for %s", path)
		}
	}
}

// The operator's restriction has to reach the handler, and a typo has to stop
// the web node rather than silently leave the operation serving.
func TestMCPDisableOperationFlag(t *testing.T) {
	cmd := &RunCommand{MCPDisableOperation: []string{"pipeline_config_set", "build_abort"}}
	if err := cmd.validateMCPDisabledOperations(); err != nil {
		t.Fatalf("real operation ids rejected: %v", err)
	}

	cmd = &RunCommand{MCPDisableOperation: []string{"pipeline_status"}}
	if err := cmd.validateMCPDisabledOperations(); err == nil {
		t.Fatal("an alias is not an operation id; expected the web node to refuse it")
	}

	cmd = &RunCommand{MCPDisableOperation: []string{"pipline_get"}}
	if err := cmd.validateMCPDisabledOperations(); err == nil {
		t.Fatal("typo accepted; the operation would stay enabled")
	}
}
