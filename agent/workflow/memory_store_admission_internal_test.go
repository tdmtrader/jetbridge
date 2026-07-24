package workflow

import (
	"errors"
	"testing"
)

type countingPromotionValidator struct {
	calls int
}

func (validator *countingPromotionValidator) ValidatePromotion(Definition) error {
	validator.calls++
	return nil
}

func TestMemoryStorePromoteNonV3RejectsBeforeCompilationOrValidator(t *testing.T) {
	validator := &countingPromotionValidator{}
	store := NewMemoryStore(validator)
	source := Manifest{"workflow.yml": `schema_version: 3
name: admission-order
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: do the work
`}
	live, err := store.ImportManifest("admission-order", source, "alice")
	if err != nil {
		t.Fatalf("import v3: %v", err)
	}
	if _, err := store.Promote("admission-order", live.Version, "alice"); err != nil {
		t.Fatalf("promote v3: %v", err)
	}
	baselineCalls := validator.calls
	if baselineCalls <= 0 {
		t.Fatalf("validator calls after v3 promotion = %d, want positive", baselineCalls)
	}

	store.mu.Lock()
	store.nextID++
	legacy := &Definition{
		ID:             store.nextID,
		Name:           "admission-order",
		Version:        live.Version + 1,
		SchemaVersion:  2,
		ContentHash:    "historical-v2",
		RawYAML:        "not: [valid YAML",
		SourceManifest: Manifest{"workflow.yml": "not: [valid YAML"},
	}
	store.defs = append(store.defs, legacy)
	store.mu.Unlock()

	_, err = store.Promote("admission-order", legacy.Version, "bob")
	var invalid InvalidPromotionError
	if !errors.As(err, &invalid) {
		t.Fatalf("error = %T %v, want InvalidPromotionError", err, err)
	}
	var unsupported UnsupportedSchemaVersionError
	if !errors.As(err, &unsupported) || unsupported.Got != 2 {
		t.Fatalf("error = %T %v, unsupported = %+v", err, err, unsupported)
	}
	const want = "workflow: version is not runnable: workflow: unsupported schema_version 2; only schema_version 3 is supported"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if validator.calls != baselineCalls {
		t.Fatalf("validator calls = %d, want unchanged baseline %d", validator.calls, baselineCalls)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	var storedV3, storedV2 *Definition
	for _, definition := range store.defs {
		switch definition.Version {
		case live.Version:
			storedV3 = definition
		case legacy.Version:
			storedV2 = definition
		}
	}
	if storedV3 == nil || !storedV3.Live {
		t.Fatal("rejected legacy promotion cleared the live v3 row")
	}
	if storedV2 == nil || storedV2.Live {
		t.Fatal("rejected legacy promotion made the legacy row live")
	}
}
