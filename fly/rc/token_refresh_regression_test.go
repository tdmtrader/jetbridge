package rc_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/concourse/concourse/fly/rc"
	"golang.org/x/oauth2"
)

const renewalTarget rc.TargetName = "renewal-regression"

func renewalJWT(t *testing.T, expiry time.Time) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"sub": "renewal-test", "exp": expiry.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".test-signature"
}

func renewalSource(t *testing.T, serverURL string) (oauth2.TokenSource, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FLY_HOME", dir)
	old := &rc.TargetToken{Type: "bearer", Value: renewalJWT(t, time.Now().Add(-time.Minute)), RefreshToken: "original-refresh-credential", OAuthClientID: rc.FlyBrowserClientID}
	if err := rc.SaveTarget(renewalTarget, serverURL, false, "main", old, "", "", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".flyrc")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	target, err := rc.LoadTarget(renewalTarget, false)
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := target.Client().HTTPClient().Transport.(*oauth2.Transport)
	if !ok {
		t.Fatal("target does not use the authenticated transport")
	}
	return transport.Source, path, before
}

func renewalResponse(w http.ResponseWriter, idToken, refreshToken string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "issuer-access-token", "token_type": "Bearer", "id_token": idToken, "refresh_token": refreshToken, "expires_in": 3600})
}

func requireSavedLoginUnchanged(t *testing.T, path string, before []byte) {
	t.Helper()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed renewal changed the persisted login")
	}
}

func TestRefreshTransientFailurePreservesCredentialsAndCanRetry(t *testing.T) {
	renewed := renewalJWT(t, time.Now().Add(time.Hour))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/sky/issuer/token" {
			t.Errorf("unexpected issuer request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.PostForm.Get("grant_type") != "refresh_token" || r.PostForm.Get("client_id") != rc.FlyBrowserClientID || r.PostForm.Get("refresh_token") != "original-refresh-credential" {
			t.Error("retry did not preserve the original client-bound refresh credential")
		}
		if requests.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "temporarily_unavailable", "error_description": "sensitive-issuer-detail original-refresh-credential"})
			return
		}
		renewalResponse(w, renewed, "rotated-refresh-credential")
	}))
	defer server.Close()
	source, path, before := renewalSource(t, server.URL)
	value, err := source.Token()
	if err == nil || value != nil || !strings.Contains(err.Error(), "temporarily unavailable") {
		t.Fatalf("expected failed temporary renewal, got token=%v error=%v", value != nil, err)
	}
	if strings.Contains(err.Error(), "sensitive-issuer-detail") || strings.Contains(err.Error(), "original-refresh-credential") {
		t.Fatal("renewal error exposed issuer response credentials")
	}
	requireSavedLoginUnchanged(t, path, before)
	value, err = source.Token()
	if err != nil || value == nil || value.AccessToken != renewed {
		t.Fatalf("retry did not recover: %v", err)
	}
	targets, err := rc.LoadTargets()
	if err != nil {
		t.Fatal(err)
	}
	saved := targets[renewalTarget].Token
	if saved.Value != renewed || saved.RefreshToken != "rotated-refresh-credential" || saved.OAuthClientID != rc.FlyBrowserClientID {
		t.Fatal("successful retry did not persist the rotated client-bound login")
	}
	if requests.Load() != 2 {
		t.Fatalf("got %d exchanges, want one failed exchange and one retry", requests.Load())
	}
}

func TestRefreshMalformedResponseNeverOverwritesSavedGrant(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		renewalResponse(w, "malformed-identity-token", "unsavable-refresh-credential")
	}))
	defer server.Close()
	source, path, before := renewalSource(t, server.URL)
	value, err := source.Token()
	if err == nil || value != nil || !strings.Contains(err.Error(), "invalid login response") {
		t.Fatalf("malformed identity response returned token=%v error=%v", value != nil, err)
	}
	requireSavedLoginUnchanged(t, path, before)
}

func TestRefreshPersistenceFailureIsReported(t *testing.T) {
	renewed := renewalJWT(t, time.Now().Add(time.Hour))
	var path string
	fault := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate an unusable configuration destination after the successful
		// exchange has begun. This works under root as well as ordinary CI users,
		// unlike a chmod-only test; retain the original bytes for verification.
		err := os.Rename(path, path+".before-fault")
		if err == nil {
			err = os.Mkdir(path, 0700)
		}
		fault <- err
		if err != nil {
			w.WriteHeader(500)
			return
		}
		renewalResponse(w, renewed, "rotated-but-not-persisted")
	}))
	defer server.Close()
	source, configPath, before := renewalSource(t, server.URL)
	path = configPath
	defer func() { _ = os.Remove(path); _ = os.Rename(path+".before-fault", path) }()
	value, err := source.Token()
	select {
	case injected := <-fault:
		if injected != nil {
			t.Fatalf("could not inject persistence failure: %v", injected)
		}
	default:
		t.Fatalf("renewal did not reach the issuer: %v", err)
	}
	if err == nil || value != nil || !strings.Contains(err.Error(), "could not save rotated credentials") {
		t.Fatalf("persistence failure returned token=%v error=%v", value != nil, err)
	}
	if strings.Contains(err.Error(), "rotated-but-not-persisted") {
		t.Fatal("persistence error exposed rotated credential")
	}
	requireSavedLoginUnchanged(t, path+".before-fault", before)
}
