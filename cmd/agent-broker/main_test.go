package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/broker/sandbox"
)

func TestBrokerHTTPHandlerServesHealthAndMCPOnly(t *testing.T) {
	handler := brokerHTTPHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			t.Fatalf("MCP path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
	}), "parent-access-token")
	for _, test := range []struct {
		path          string
		authorization string
		want          int
	}{
		{"/healthz", "", http.StatusOK},
		{"/mcp", "", http.StatusUnauthorized},
		{"/mcp", "Bearer wrong", http.StatusUnauthorized},
		{"/mcp", "Bearer parent-access-token", http.StatusAccepted},
		{"/", "", http.StatusNotFound},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", test.authorization)
		handler.ServeHTTP(recorder, request)
		if recorder.Code != test.want {
			t.Fatalf("%s status = %d, want %d", test.path, recorder.Code, test.want)
		}
	}
}

func TestLoadConfigRejectsUnknownFieldsAndInlineSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")
	if err := os.WriteFile(path, []byte(`{"authority_endpoint":"http://127.0.0.1:8080","bootstrap_capability":"secret","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("loadConfig() error = %v", err)
	}
}

func TestLoadConfigRejectsRelativeAuthorityFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")
	if err := os.WriteFile(path, []byte(`{"authority_endpoint":"http://127.0.0.1:8080","bootstrap_capability_file":"relative"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(path)
	if err == nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
}

func TestValidateExactKeysRejectsOrphanConfigurationEntries(t *testing.T) {
	tests := []struct {
		name    string
		values  map[string]string
		allowed []string
	}{
		{"adapter", map[string]string{"codex": "/bin/codex", "orphan": "/bin/nope"}, []string{"codex", "claude", "cursor-agent"}},
		{"profile digest", map[string]string{"profile": "sha256:x", "orphan": "sha256:y"}, []string{"profile"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateExactKeys(test.name, test.values, test.allowed); err == nil || !strings.Contains(err.Error(), "orphan") {
				t.Fatalf("validateExactKeys() error = %v", err)
			}
		})
	}
}

func TestPinnedProbeRunsVersionThroughPerRunSandbox(t *testing.T) {
	scratchRoot := t.TempDir()
	binary := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(binary, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtimePath := t.TempDir()
	var captured sandbox.Policy
	probe := pinnedProbe{
		paths:       map[string]string{"codex": binary},
		scratchRoot: scratchRoot,
		readPaths:   []string{runtimePath},
		sandboxOutput: func(
			_ context.Context,
			gotBinary string,
			arguments []string,
			environment []string,
			policy sandbox.Policy,
		) ([]byte, error) {
			if gotBinary != binary || strings.Join(arguments, " ") != "--version" ||
				strings.Join(environment, " ") != "LC_ALL=C" {
				t.Fatalf("sandbox probe = (%q, %#v, %#v)", gotBinary, arguments, environment)
			}
			captured = policy
			return []byte("codex-cli 0.146.0\n"), nil
		},
	}
	output, err := probe.Output(context.Background(), binary, []string{"--version"}, []string{"LC_ALL=C"})
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "codex-cli 0.146.0\n" ||
		filepath.Dir(captured.WritableRoot) != scratchRoot ||
		len(captured.ReadOnlyPaths) != 1 || captured.ReadOnlyPaths[0] != runtimePath {
		t.Fatalf("sandbox probe output/policy = %q / %#v", output, captured)
	}
	if entries, err := os.ReadDir(scratchRoot); err != nil || len(entries) != 0 {
		t.Fatalf("sandbox preflight scratch cleanup = %#v, %v", entries, err)
	}
}
