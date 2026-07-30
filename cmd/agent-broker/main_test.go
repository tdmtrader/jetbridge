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
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("loadConfig() error = %v", err)
	}
}
