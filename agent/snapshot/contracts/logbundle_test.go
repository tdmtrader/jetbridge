package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestOpaqueContractRequiresNonemptyTree(t *testing.T) {
	if _, err := validateFiles(t, "opaque/v1", map[string][]byte{"value.bin": {0, 1, 2}}, emptyValidationContext(t)); err != nil {
		t.Fatalf("nonempty opaque tree error = %v", err)
	}
	if _, err := validateFiles(t, "opaque/v1", nil, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty opaque tree error = %v, want non-empty", err)
	}
}

func TestLogBundleRequiresLogFileAndStrictOptionalMetadata(t *testing.T) {
	metadata := contracts.LogBundleMetadata{SchemaVersion: "1.0.0", CapturedAt: "2026-07-22T12:00:00Z", Source: "task output"}
	if _, err := validateFiles(t, "log-bundle/v1", map[string][]byte{
		"metadata.json": marshalDocument(t, metadata), "logs/task.log": []byte("complete\n"),
	}, emptyValidationContext(t)); err != nil {
		t.Fatalf("valid log bundle error = %v", err)
	}
	if _, err := validateFiles(t, "log-bundle/v1", map[string][]byte{"metadata.json": marshalDocument(t, metadata)}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "log") {
		t.Fatalf("metadata-only bundle error = %v, want log-file error", err)
	}
	metadata.SchemaVersion = "1.0.1"
	if _, err := validateFiles(t, "log-bundle/v1", map[string][]byte{
		"metadata.json": marshalDocument(t, metadata), "task.log": []byte("complete\n"),
	}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "1.0.0") {
		t.Fatalf("metadata version error = %v, want exact version", err)
	}
	metadata.SchemaVersion = "1.0.0"
	metadata.CapturedAt = "today"
	if _, err := validateFiles(t, "log-bundle/v1", map[string][]byte{
		"metadata.json": marshalDocument(t, metadata), "task.log": []byte("complete\n"),
	}, emptyValidationContext(t)); err == nil || !strings.Contains(err.Error(), "captured_at") {
		t.Fatalf("metadata captured_at error = %v, want RFC3339 error", err)
	}
}
