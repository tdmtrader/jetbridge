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
	r := httptest.NewRequest(http.MethodGet, "/api/v1/user", nil)
	r.AddCookie(&http.Cookie{Name: "skymarshal_auth", Value: "browser-login"})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if apiAuthorization != "browser-login" || len(w.Result().Cookies()) == 0 {
		t.Fatal("existing API cookie authentication no longer works")
	}
}
