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

func TestMemoryStoreHistoricalNonV3ReadsRemainOpaque(t *testing.T) {
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

	const (
		legacyRaw         = "not: [valid YAML"
		legacyHash        = "historical-v2"
		legacyDescription = "opaque schema-2 history"
		legacyCreatedBy   = "historical-import"
		legacyCreatedAt   = int64(1_700_000_000)
	)
	store.mu.Lock()
	store.nextID++
	legacy := &Definition{
		ID:               store.nextID,
		Name:             "admission-order",
		Version:          live.Version + 1,
		SchemaVersion:    2,
		SignatureVersion: 0,
		ContentHash:      legacyHash,
		Description:      legacyDescription,
		CreatedBy:        legacyCreatedBy,
		CreatedAt:        legacyCreatedAt,
		RawYAML:          legacyRaw,
		SourceManifest:   Manifest{"workflow.yml": legacyRaw},
	}
	store.defs = append(store.defs, legacy)
	store.mu.Unlock()

	assertOpaque := func(label string, got *Definition, found bool, err error) {
		t.Helper()
		if err != nil || !found {
			t.Fatalf("%s: found=%v err=%v", label, found, err)
		}
		if got.ID != legacy.ID || got.Name != legacy.Name || got.Version != legacy.Version ||
			got.SchemaVersion != legacy.SchemaVersion || got.SignatureVersion != legacy.SignatureVersion ||
			got.ContentHash != legacyHash || got.Description != legacyDescription ||
			got.CreatedBy != legacyCreatedBy || got.CreatedAt != legacyCreatedAt || got.Live {
			t.Fatalf("%s metadata = %+v, want exact historical metadata %+v", label, got, legacy)
		}
		if got.RawYAML != legacyRaw {
			t.Fatalf("%s RawYAML = %q, want %q", label, got.RawYAML, legacyRaw)
		}
		if got.SourceManifest != nil {
			t.Fatalf("%s SourceManifest = %+v, want nil", label, got.SourceManifest)
		}
		if got.Compiled != (CompiledDefinition{}) {
			t.Fatalf("%s Compiled = %+v, want zero", label, got.Compiled)
		}
	}
	got, found, err := store.Get("admission-order", legacy.Version)
	assertOpaque("Get", got, found, err)
	latest, found, err := store.Latest("admission-order")
	assertOpaque("Latest", latest, found, err)

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
