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

// validateFiles drives the SEAL-TIME gate: the files are a candidate tree a step
// has just written, which is what almost every contract test is about.
func validateFiles(t *testing.T, rawType string, files map[string][]byte, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	t.Helper()
	return validateDirectory(t, rawType, writeTree(t, files), validationContext)
}

// revalidateSealedFiles drives the READ-TIME gate over the same tree, for tests
// about whether a stored record is still readable.
func revalidateSealedFiles(t *testing.T, rawType string, files map[string][]byte, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	t.Helper()
	return withValidator(t, rawType, writeTree(t, files), func(validator snapshot.Validator, root *os.Root) (snapshot.ValidationResult, error) {
		return validator.RevalidateSealed(context.Background(), root, validationContext)
	})
}

func writeTree(t *testing.T, files map[string][]byte) string {
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
	return dir
}

func validateDirectory(t *testing.T, rawType, dir string, validationContext snapshot.ValidationContext) (snapshot.ValidationResult, error) {
	t.Helper()
	return withValidator(t, rawType, dir, func(validator snapshot.Validator, root *os.Root) (snapshot.ValidationResult, error) {
		return validator.AdmitForSeal(context.Background(), root, validationContext)
	})
}

func withValidator(
	t *testing.T,
	rawType, dir string,
	gate func(snapshot.Validator, *os.Root) (snapshot.ValidationResult, error),
) (snapshot.ValidationResult, error) {
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
	return gate(validator, root)
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

// candidateValidationContextFor exposes inputs and declares which of them are
// candidate ports. Candidacy is a server-side port declaration, so tests must
// supply it here rather than inside the record under validation.
func candidateValidationContextFor(
	t *testing.T,
	inputs map[string]snapshot.SnapshotRef,
	candidatePorts ...string,
) snapshot.ValidationContext {
	t.Helper()
	context, err := snapshot.NewValidationContext(inputs, nil, snapshot.WithCandidatePorts(candidatePorts...))
	if err != nil {
		t.Fatalf("NewValidationContext(candidate ports %q): %v", candidatePorts, err)
	}
	return context
}
