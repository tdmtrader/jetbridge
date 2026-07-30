package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
