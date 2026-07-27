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

// Exposure lineage must reach the commit context as server-declared occurrence
// data while staying entirely out of content identity. IntrinsicMetadata is
// compared for agreement per (type_name, type_version, digest) — sealer.go:535,
// sealer.go:571, store.go:385, store.go:835, and the SQL at
// atc/db/agent_snapshots_factory.go:498-502 — so it has no version-bump path,
// and any exposure detail leaking into it would corrupt the corpus.
func TestSealCarriesExposureLineageToTheCommitButNotIntoContentIdentity(t *testing.T) {
	now := time.Date(2026, time.July, 25, 12, 0, 0, 0, time.UTC)
	body := tarBytes(t, "value", "same")
	inputDigest := mustTestDigest(t)

	sealOnce := func(exposures map[string]InputExposure) *SealCommit {
		metadata := &sealerMetadataStore{}
		content := &sealerContentStore{events: &metadata.events}
		locks := &sealerLocks{lease: &sealerLease{digests: map[Digest]bool{}}}
		sealer := mustNewSealer(t, t.TempDir(), metadata, content, locks,
			sealerValidatorFunc(func(context.Context, *os.Root, ValidationContext) (ValidationResult, error) {
				return ValidationResult{IntrinsicMetadata: json.RawMessage(`{"kind":"opaque"}`)}, nil
			}),
			WithBatchSealerClock(func() time.Time { return now }),
		)
		request := sealerRequest([]OutputSource{sealerSource("result", "result", "opaque/v1", body)})
		request.InputOrder = []string{"base"}
		request.Inputs = map[string]SnapshotRef{
			"base": {ID: 5, Type: TypeRef("repository/v1"), Digest: inputDigest},
		}
		request.InputExposures = exposures
		if _, err := sealer.Seal(context.Background(), request); err != nil {
			t.Fatalf("Seal() error = %v", err)
		}
		return metadata.commit
	}

	mounted := FullTreeExposure("/tmp/build/plan/base", inputDigest)
	declared := sealOnce(map[string]InputExposure{"base": mounted})
	// An input with no declaration is not "unknown": the step could have read
	// all of it, so the honest default is the whole tree with no mount path.
	defaulted := sealOnce(nil)

	// Same bytes, different exposure declaration: identical content identity.
	if declared.Outputs[0].Digest != defaulted.Outputs[0].Digest {
		t.Fatalf("exposure changed the sealed digest: %s vs %s", declared.Outputs[0].Digest, defaulted.Outputs[0].Digest)
	}
	for _, commit := range []*SealCommit{declared, defaulted} {
		intrinsic := string(commit.Outputs[0].IntrinsicMetadata)
		if intrinsic != `{"kind":"opaque"}` {
			t.Fatalf("intrinsic metadata = %s, want exactly the validator's result", intrinsic)
		}
		for _, forbidden := range []string{"mount", "materializ", "tree_digest", "full"} {
			if strings.Contains(intrinsic, forbidden) {
				t.Fatalf("intrinsic metadata leaked exposure detail %q: %s", forbidden, intrinsic)
			}
		}
	}
	if !reflect.DeepEqual(declared.Context.Exposures()["base"], mounted) {
		t.Fatalf("committed exposure = %+v, want the declared mount %+v", declared.Context.Exposures()["base"], mounted)
	}
	if got := defaulted.Context.Exposures()["base"]; got != FullTreeExposure("", inputDigest) {
		t.Fatalf("committed exposure = %+v, want the whole tree with no mount path", got)
	}
}
