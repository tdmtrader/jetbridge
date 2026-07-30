package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
