package contracts_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func validateFiles(t *testing.T, rawType string, files map[string][]byte, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		fullPath := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			t.Fatalf("mkdir %q: %v", name, err)
		}
		if err := os.WriteFile(fullPath, contents, 0644); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	return validateDirectory(t, rawType, dir, validationContext)
}

func validateDirectory(t *testing.T, rawType, dir string, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%q): %v", dir, err)
	}
	defer root.Close()
	registry, err := contracts.NewRegistry(
		contracts.WithCanonicalizer(snapshot.Canonicalizer{TempDir: t.TempDir()}),
	)
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	validator, err := registry.Lookup(mustTypeRef(t, rawType))
	if err != nil {
		t.Fatalf("Lookup(%q): %v", rawType, err)
	}
	return validator.Validate(context.Background(), root, validationContext)
}

func emptyValidationContext(t *testing.T) snapshot.ValidationContext {
	t.Helper()
	validationContext, err := snapshot.NewValidationContext(nil, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	return validationContext
}

func marshalRecord[T any](t *testing.T, record contracts.Record[T]) []byte {
	t.Helper()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	return encoded
}

func validationContextFor(t *testing.T, inputs map[string]snapshot.SnapshotRef) snapshot.ValidationContext {
	t.Helper()
	context, err := snapshot.NewValidationContext(inputs, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	return context
}
