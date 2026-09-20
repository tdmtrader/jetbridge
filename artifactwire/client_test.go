package artifactwire

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func plainClient(t *testing.T, port int) *Client {
	t.Helper()
	c, err := NewClient(port, TLS{})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestURLAssembly(t *testing.T) {
	for _, tc := range []struct {
		name string
		port int
		host string
		path string
		want string
	}{
		{"default port", 0, "10.0.0.5", ArtifactsPrefix + "art-key", "http://10.0.0.5:7780/artifacts/art-key"},
		{"explicit port", 8080, "10.0.0.5", Register.Path, "http://10.0.0.5:8080/register"},
		{"steps key keeps its slash", 0, "10.0.0.5", ArtifactsPrefix + StepsPrefix + "h", "http://10.0.0.5:7780/artifacts/steps/h"},
		{"stream in is the same key space", 0, "10.0.0.5", StreamInPrefix + "art-key", "http://10.0.0.5:7780/stream-in/art-key"},
		{"ipv6 host is bracketed", 0, "fd00::5", Mirror.Path, "http://[fd00::5]:7780/mirror"},
	} {
		if got := plainClient(t, tc.port).URL(tc.host, tc.path); got != tc.want {
			t.Errorf("%s: URL = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A partial triple is plaintext here; the flags refuse it at startup, and this
// package must not dial https while presenting nothing.
func TestTLSConfiguredNeedsTheWholeTriple(t *testing.T) {
	for _, tc := range []struct {
		name string
		tls  TLS
		want bool
	}{
		{"all three", TLS{CertPath: "c", KeyPath: "k", CACertPath: "a"}, true},
		{"nothing", TLS{}, false},
		{"cert only", TLS{CertPath: "c"}, false},
		{"cert and key", TLS{CertPath: "c", KeyPath: "k"}, false},
		{"key and ca", TLS{KeyPath: "k", CACertPath: "a"}, false},
	} {
		if got := tc.tls.Configured(); got != tc.want {
			t.Errorf("%s: Configured = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestPlainClientDialsHTTP(t *testing.T) {
	c := plainClient(t, 0)
	if c.Scheme() != "http" || c.Port() != DefaultPort {
		t.Errorf("plain client: scheme %q port %d", c.Scheme(), c.Port())
	}
	if c.plain.Timeout != 0 || c.streaming.Timeout != 0 || c.restore.Timeout != 0 {
		t.Error("a whole-request timeout would sever a long tar stream mid-read")
	}
	if c.streaming.Transport.(*http.Transport).ResponseHeaderTimeout <= 0 {
		t.Error("the streaming transport must bound the wait for headers")
	}
}

func TestUnloadableTLSIsAnError(t *testing.T) {
	_, err := NewClient(0, TLS{CertPath: "/nonexistent/c", KeyPath: "/nonexistent/k", CACertPath: "/nonexistent/a"})
	if err == nil {
		t.Fatal("a triple that cannot be loaded produced a client; it would dial https presenting nothing")
	}
}

// Status classification is the only place a number becomes a meaning.
func TestRefusalClassification(t *testing.T) {
	for _, tc := range []struct {
		status                               int
		refused, notFound, held, unavailable bool
	}{
		{http.StatusBadRequest, true, false, false, false},
		{http.StatusForbidden, true, false, false, false},
		{http.StatusNotFound, true, true, false, false},
		{http.StatusConflict, true, false, true, false},
		{http.StatusUnprocessableEntity, true, false, false, false},
		{http.StatusInternalServerError, false, false, false, true},
		{http.StatusServiceUnavailable, false, false, false, true},
	} {
		var err error = &Refusal{Method: "POST", URL: "http://d/register", Status: tc.status}
		if got := errors.Is(err, ErrRefused); got != tc.refused {
			t.Errorf("%d: ErrRefused=%v want %v", tc.status, got, tc.refused)
		}
		if got := errors.Is(err, ErrNotFound); got != tc.notFound {
			t.Errorf("%d: ErrNotFound=%v want %v", tc.status, got, tc.notFound)
		}
		if got := errors.Is(err, ErrHeld); got != tc.held {
			t.Errorf("%d: ErrHeld=%v want %v", tc.status, got, tc.held)
		}
		if got := errors.Is(err, ErrUnavailable); got != tc.unavailable {
			t.Errorf("%d: ErrUnavailable=%v want %v", tc.status, got, tc.unavailable)
		}
	}
}

func TestRefusalErrorNamesTheRequest(t *testing.T) {
	err := &Refusal{Method: "DELETE", URL: "http://d/artifacts/steps/h", Status: 409, Body: "held"}
	for _, want := range []string{"DELETE", "http://d/artifacts/steps/h", "409", "held"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err.Error(), want)
		}
	}
}

// The prelude is the three lines every init-container script opens with, and
// they say what the Go client says.
func TestShellPrelude(t *testing.T) {
	c := plainClient(t, 7780)
	want := "PORT=7780\nDAEMON=\"http://${HOST_IP}:${PORT}\"\nWGET_OPTS=\"\"\n"
	if got := c.ShellPrelude(); got != want {
		t.Errorf("prelude:\n%s\nwant:\n%s", got, want)
	}
	if c.WgetOptions() != "" {
		t.Errorf("plaintext client renders wget options %q", c.WgetOptions())
	}

	https := &Client{scheme: "https", port: 8443}
	if got := https.ShellPrelude(); !strings.Contains(got, "https://${HOST_IP}") || !strings.Contains(got, "--no-check-certificate") {
		t.Errorf("https prelude:\n%s", got)
	}
}

// A misconfigured client is not a plaintext client. It keeps the scheme the
// deployment asked for and refuses every request naming the reason.
func TestMisconfiguredClientRefusesEveryRequest(t *testing.T) {
	c := Misconfigured(0, errors.New("load daemon client certificate: open /nonexistent/client.crt: no such file"))
	if c.Scheme() != "https" || c.Port() != DefaultPort {
		t.Errorf("misconfigured client: scheme %q port %d", c.Scheme(), c.Port())
	}
	if !strings.Contains(c.ShellPrelude(), "https://") || !strings.Contains(c.ShellPrelude(), "--no-check-certificate") {
		t.Errorf("prelude does not render what the deployment asked for:\n%s", c.ShellPrelude())
	}
	err := c.Mirror(context.Background(), "10.0.0.5", "k")
	if err == nil || !strings.Contains(err.Error(), "client cert") {
		t.Fatalf("expected every request to fail naming the certificate, got %v", err)
	}
}
