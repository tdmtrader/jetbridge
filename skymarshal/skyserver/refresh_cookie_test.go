package skyserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"code.cloudfoundry.org/lager/v3/lagertest"
	"github.com/concourse/concourse/skymarshal/skyserver"
	"github.com/concourse/concourse/skymarshal/token"
	"golang.org/x/oauth2"
)

func TestNonrenewableLoginClearsPreviousRefreshCookie(t *testing.T) {
	server, err := skyserver.NewSkyServer(&skyserver.SkyConfig{
		Logger:          lagertest.NewTestLogger("refresh-cookie"),
		TokenMiddleware: token.NewMiddleware(true),
		StateSigningKey: make([]byte, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "https://jetbridge.example/sky/callback", nil)
	r.AddCookie(&http.Cookie{Name: "skymarshal_refresh", Value: "previous-account-refresh"})
	w := httptest.NewRecorder()
	newLogin := (&oauth2.Token{AccessToken: "new-account-access", TokenType: "bearer", Expiry: time.Now().Add(time.Hour)}).WithExtra(map[string]any{"id_token": "new-account-identity"})
	server.Redirect(w, r, newLogin, "/")
	if w.Code != http.StatusTemporaryRedirect {
		t.Fatalf("login status %d", w.Code)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name != "skymarshal_refresh" {
			continue
		}
		if cookie.Value != "" || cookie.MaxAge >= 0 || cookie.Path != "/sky/" || !cookie.Secure || !cookie.HttpOnly {
			t.Fatalf("previous refresh cookie was not safely removed: %#v", cookie)
		}
		return
	}
	t.Fatal("nonrenewable login left the previous refresh cookie in place")
}
