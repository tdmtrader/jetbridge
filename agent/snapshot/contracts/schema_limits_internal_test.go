package contracts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

// These tests are inside the package because the limits they drive are
// unexported. They therefore carry their own seal-gate driver rather than
// helpers_test.go's validateFiles, which lives in the external test package.
func admitTreeForSeal(t *testing.T, ref snapshot.TypeRef, dir string, declarations snapshot.ValidationContext) error {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot(%q): %v", dir, err)
	}
	defer root.Close()
	registry, err := NewRegistry(WithCanonicalizer(snapshot.Canonicalizer{TempDir: t.TempDir()}))
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	validator, err := registry.Lookup(ref)
	if err != nil {
		t.Fatalf("Lookup(%q): %v", ref, err)
	}
	_, err = validator.AdmitForSeal(context.Background(), root, declarations)
	return err
}

func writeCandidateTree(t *testing.T, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	for name, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", name, err)
		}
		if err := os.WriteFile(full, contents, 0o644); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
	}
	return dir
}

// withJSONDocumentLimit lowers the strict-document limit for one test and puts
// it back afterwards. It mutates package state, so it must never be called from
// a test that also calls t.Parallel(); no test in this package does.
func withJSONDocumentLimit(t *testing.T, limit int64) {
	t.Helper()
	original := maxJSONDocumentBytes
	maxJSONDocumentBytes = limit
	t.Cleanup(func() { maxJSONDocumentBytes = original })
}

func withRepositoryPayloadLimit(t *testing.T, limit int64) {
	t.Helper()
	original := maxRepositoryPayloadBytes
	maxRepositoryPayloadBytes = limit
	t.Cleanup(func() { maxRepositoryPayloadBytes = original })
}

// TestStrictDocumentLimitAdmitsExactlyItsOwnSizeAndNoMore pins the boundary of
// maxJSONDocumentBytes at the seal gate. The candidate's own encoded size is the
// pivot: with the limit set to exactly that size the record admits, and with the
// limit one byte lower it is rejected. An off-by-one in the readRegularFile
// comparison ( > versus >= ) flips exactly one of the two assertions.
func TestStrictDocumentLimitAdmitsExactlyItsOwnSizeAndNoMore(t *testing.T) {
	ref := snapshot.SnapshotRef{ID: 51, Type: repositoryChangeType, Digest: fixtureDigest('a')}
	declarations, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{"change": ref}, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	record, err := NewRecord(reviewType,
		[]Subject{SubjectFromInput("primary", SubjectRolePrimary, "change", ref)},
		ReviewBody{Conclusion: "accept", Summary: "nothing to change", Findings: []Finding{}},
	)
	if err != nil {
		t.Fatalf("NewRecord(review/v1): %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	dir := writeCandidateTree(t, map[string][]byte{"record.json": encoded})
	size := int64(len(encoded))

	t.Run("at exactly its own size", func(t *testing.T) {
		withJSONDocumentLimit(t, size)
		if err := admitTreeForSeal(t, reviewType, dir, declarations); err != nil {
			t.Fatalf("a record.json of exactly the limit was rejected: %v", err)
		}
	})
	t.Run("one byte under its own size", func(t *testing.T) {
		withJSONDocumentLimit(t, size-1)
		err := admitTreeForSeal(t, reviewType, dir, declarations)
		if err == nil || !strings.Contains(err.Error(), "exceeds size limit of") {
			t.Fatalf("a record.json one byte over the limit: error = %v, want it to name the size limit", err)
		}
	})
}

// TestRepositoryPayloadLimitAdmitsExactlyItsOwnSizeAndNoMore pins the boundary of
// maxRepositoryPayloadBytes the same way, one byte either side of the payload's
// own size.
//
// The payload limit is enforced in spoolRepositoryPayload, before any git
// command runs, so exercising it needs no repository at all. Driving the whole
// repository-change gate here would only add a git init to a test about one
// comparison.
func TestRepositoryPayloadLimitAdmitsExactlyItsOwnSizeAndNoMore(t *testing.T) {
	patch := []byte("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -0,0 +1 @@\n+hello\n")
	dir := writeCandidateTree(t, map[string][]byte{"payload": patch})
	size := int64(len(patch))

	spool := func(t *testing.T) (repositoryPayload, error) {
		t.Helper()
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatalf("OpenRoot(%q): %v", dir, err)
		}
		defer root.Close()
		return spoolRepositoryPayload(context.Background(), root, "payload", t.TempDir())
	}

	t.Run("at exactly its own size", func(t *testing.T) {
		withRepositoryPayloadLimit(t, size)
		payload, err := spool(t)
		if err != nil {
			t.Fatalf("a payload of exactly the limit was rejected: %v", err)
		}
		if err := payload.Close(); err != nil {
			t.Fatalf("close spooled payload: %v", err)
		}
	})
	t.Run("one byte under its own size", func(t *testing.T) {
		withRepositoryPayloadLimit(t, size-1)
		payload, err := spool(t)
		if err == nil {
			_ = payload.Close()
			t.Fatalf("a payload one byte over the limit was admitted")
		}
		if !strings.Contains(err.Error(), "payload exceeds size limit of") {
			t.Fatalf("payload gate error = %v, want it to name the size limit", err)
		}
	})
}

// A review carrying ten thousand findings is 1.49 MB of record.json, so it does
// not fit under the ordinary document limit — which is the finding, not an
// inconvenience: no entity set declares a cardinality bound, and the document
// limit is currently the only thing bounding one. This test raises that limit
// deliberately so the remaining question is the one it is about, namely whether
// the validators stay linear. They do: the measured time is around 30 ms, so a
// quadratic regression at this size would blow the ceiling below by orders of
// magnitude rather than by a flaky margin.
func TestAReviewCarryingTenThousandFindingsValidatesQuickly(t *testing.T) {
	withJSONDocumentLimit(t, 64<<20)

	const count = 10000
	const ceiling = 60 * time.Second

	ref := snapshot.SnapshotRef{ID: 71, Type: snapshot.TypeRef("repository-change/v1"), Digest: fixtureDigest('a')}
	declarations, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{"change": ref}, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}

	findings := make([]Finding, 0, count)
	for index := 1; index <= count; index++ {
		// Zero-padded, so lexicographic order is numeric order and the entity-set
		// sort rule is satisfied by construction. Observation severity needs no
		// evidence and may not be blocking, so the instance stays valid at any size.
		findings = append(findings, Finding{
			ID: fmt.Sprintf("f-%06d", index), Severity: "observation", Category: "style",
			Title: "naming", Description: "prefer a fuller name",
		})
	}
	record, err := NewRecord(reviewType,
		[]Subject{SubjectFromInput("primary", SubjectRolePrimary, "change", ref)},
		ReviewBody{Conclusion: "inconclusive", Summary: "ten thousand observations", Findings: findings},
	)
	if err != nil {
		t.Fatalf("NewRecord(review/v1): %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	dir := writeCandidateTree(t, map[string][]byte{"record.json": encoded})

	started := time.Now()
	if err := admitTreeForSeal(t, reviewType, dir, declarations); err != nil {
		t.Fatalf("a %d-finding review was rejected: %v", count, err)
	}
	elapsed := time.Since(started)
	t.Logf("%d findings, %d bytes of record.json, validated in %s", count, len(encoded), elapsed.Round(time.Millisecond))
	if elapsed > ceiling {
		t.Fatalf("validating %d findings took %s, over the %s ceiling; a validator has gone superlinear", count, elapsed, ceiling)
	}
}
