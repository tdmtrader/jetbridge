package snapshot

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exposure lineage must reach a validator as server-declared authority while
// staying entirely out of content identity. IntrinsicMetadata is compared for
// agreement per (type_name, type_version, digest) — sealer.go:535, sealer.go:571,
// store.go:385, store.go:835, and the SQL at
// atc/db/agent_snapshots_factory.go:498-502 — so it has no version-bump path,
// and any exposure detail leaking into it would corrupt the corpus.
func TestSealCarriesExposureLineageToValidatorsButNotIntoContentIdentity(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "same")
	inputDigest := mustTestDigest(t)

	var seen []InputExposure
	sealOnce := func(exposure InputExposure) *SealCommit {
		metadata := &sealerMetadataStore{}
		content := &sealerContentStore{events: &metadata.events}
		locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
		sealer := mustNewSealer(t, t.TempDir(), metadata, content, locks,
			sealerValidatorFunc(func(_ context.Context, _ *os.Root, validationContext ValidationContext) (ValidationResult, error) {
				got, found := validationContext.Exposure("base")
				if !found {
					t.Fatal("validator was given no exposure for the exposed input")
				}
				seen = append(seen, got)
				return ValidationResult{IntrinsicMetadata: json.RawMessage(`{"kind":"opaque"}`)}, nil
			}),
			WithBatchSealerClock(func() time.Time { return now }),
		)
		request := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})
		request.InputOrder = []string{"base"}
		request.Inputs = map[string]SnapshotRef{
			"base": {ID: 5, Type: TypeRef("repository/v1"), Digest: inputDigest},
		}
		request.InputExposures = map[string]InputExposure{"base": exposure}
		if _, err := sealer.Seal(context.Background(), request); err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		return metadata.commit
	}

	selector, err := NewStaticSelectorExposure("/tmp/build/plan/base", inputDigest,
		ExposedPath{Path: "record.json", Digest: mustTestDigest(t)},
	)
	if err != nil {
		t.Fatalf("NewStaticSelectorExposure() error = %v", err)
	}
	selective := sealOnce(selector)
	whole := sealOnce(FullTreeExposure("/tmp/build/plan/base", inputDigest))

	if len(seen) != 2 || seen[0].Mode != MaterializationStaticSelector || seen[1].Mode != MaterializationFull {
		t.Fatalf("validator saw exposures %+v, want the declared selector then the whole tree", seen)
	}
	if len(seen[0].Paths) != 1 || seen[0].Paths[0].Path != "record.json" {
		t.Fatalf("validator saw exposed paths %+v, want the exact declared path set", seen[0].Paths)
	}

	// Same bytes, different exposure: identical content identity.
	if selective.Outputs[0].Digest != whole.Outputs[0].Digest {
		t.Fatalf("exposure changed the sealed digest: %s vs %s", selective.Outputs[0].Digest, whole.Outputs[0].Digest)
	}
	for _, commit := range []*SealCommit{selective, whole} {
		intrinsic := string(commit.Outputs[0].IntrinsicMetadata)
		if intrinsic != `{"kind":"opaque"}` {
			t.Fatalf("intrinsic metadata = %s, want exactly the validator's result", intrinsic)
		}
		for _, forbidden := range []string{"mount", "materializ", "record.json", "static-selector", "full"} {
			if strings.Contains(intrinsic, forbidden) {
				t.Fatalf("intrinsic metadata leaked exposure detail %q: %s", forbidden, intrinsic)
			}
		}
	}
	if !reflect.DeepEqual(selective.Context.Exposures()["base"], selector) {
		t.Fatalf("committed exposure = %+v, want the declared selector %+v", selective.Context.Exposures()["base"], selector)
	}
	if got := whole.Context.Exposures()["base"]; got.Mode != MaterializationFull || len(got.Paths) != 0 {
		t.Fatalf("committed exposure = %+v, want whole-tree materialization", got)
	}
}
