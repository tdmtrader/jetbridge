package contracts

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// These tests live inside the package for the same reason the schema-history
// tests do: exercising a SUPERSEDED schema digest requires a type with two
// revisions, and bumping a real descriptor is the thing this machinery exists to
// make safe rather than the thing to do in a test. The synthetic two-revision
// history from record_schema_internal_test.go is substituted for the frozen
// table instead.
//
// What is under test is the split between the two gates:
//
//   - Seal-time admission judges an agent-authored candidate. Accepting a
//     superseded digest there would let a producer pick which contract identity
//     its own output advertises — authority created out of a producer's claim —
//     and would falsify the promise the runner prompt makes to the agent, that
//     the exact values it was handed are verified again when the output is sealed
//     (agent/runner/runner.go, "# Sealed record authority").
//
//   - Read-time revalidation judges bytes the platform already sealed. Refusing
//     a superseded digest there would turn a descriptor bump into retroactive
//     corruption of the stored corpus.

// syntheticDeclarations exposes exactly the input syntheticSubjects() binds to,
// so seal-time admission gets past subject binding and turns on the schema
// digest alone.
func syntheticDeclarations(t *testing.T) snapshot.ValidationContext {
	t.Helper()
	subject := syntheticSubjects()[0]
	declarations, err := snapshot.NewValidationContext(map[string]snapshot.SnapshotRef{
		subject.Input: {ID: 1, Type: subject.Type, Digest: subject.Digest},
	}, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	return declarations
}

// syntheticRecordPinning builds a record of the synthetic type carrying an
// arbitrary schema digest, standing in for bytes some producer wrote.
func syntheticRecordPinning(t *testing.T, schema snapshot.Digest) Record[json.RawMessage] {
	t.Helper()
	record, err := NewRecord(syntheticRecordType, syntheticSubjects(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	record.Schema = schema
	return record
}

func TestSealTimeAdmissionRejectsASupersededSchemaDigest(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())
	superseded := schemaDescriptorDigest(syntheticRevision1)
	if !IsAcceptedSchemaDigest(syntheticRecordType, superseded) {
		t.Fatal("test setup: revision 1 is not an accepted digest")
	}
	record := syntheticRecordPinning(t, superseded)
	declarations := syntheticDeclarations(t)

	if err := record.AdmitForSeal(syntheticRecordType, declarations); err == nil {
		t.Fatal("AdmitForSeal() admitted a candidate pinning a superseded schema digest; " +
			"a producer must not get to choose which contract identity its own output advertises")
	} else if !strings.Contains(err.Error(), "current") {
		t.Fatalf("AdmitForSeal() error = %v, want it to name the current schema digest", err)
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var decoded Record[json.RawMessage]
	if err := DecodeRecordForSeal(encoded, syntheticRecordType, &decoded); err == nil {
		t.Fatal("DecodeRecordForSeal() admitted bytes pinning a superseded schema digest")
	}
}

func TestReadTimeRevalidationAcceptsASupersededSchemaDigest(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())
	superseded := schemaDescriptorDigest(syntheticRevision1)
	record := syntheticRecordPinning(t, superseded)

	if err := record.RevalidateSealed(syntheticRecordType); err != nil {
		t.Fatalf("RevalidateSealed() rejected a stored record sealed under a superseded schema digest: %v; "+
			"a descriptor bump is a versioning event, not retroactive corruption", err)
	}

	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var decoded Record[json.RawMessage]
	if err := DecodeSealedRecord(encoded, syntheticRecordType, &decoded); err != nil {
		t.Fatalf("DecodeSealedRecord() rejected a stored superseded-digest record: %v", err)
	}
	if decoded.Schema != superseded {
		t.Fatalf("decoded schema = %q, want %q preserved", decoded.Schema, superseded)
	}
}

func TestSealTimeAdmissionAcceptsTheCurrentSchemaDigest(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())
	current := schemaDescriptorDigest(syntheticRevision2)
	record := syntheticRecordPinning(t, current)
	declarations := syntheticDeclarations(t)

	if err := record.AdmitForSeal(syntheticRecordType, declarations); err != nil {
		t.Fatalf("AdmitForSeal() rejected a candidate pinning the current schema digest: %v", err)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var decoded Record[json.RawMessage]
	if err := DecodeRecordForSeal(encoded, syntheticRecordType, &decoded); err != nil {
		t.Fatalf("DecodeRecordForSeal() rejected bytes pinning the current schema digest: %v", err)
	}
}

// TestSealTimeAdmissionRequiresExactlyTheDigestTheAgentWasHanded pins the promise
// the runner prompt makes: AGENT_OUTPUT_<PORT>_RECORD_SCHEMA is computed by
// SchemaDigestFor, and seal-time admission must accept that value and only that
// value. If the two ever diverge, the prompt is lying to the agent.
func TestSealTimeAdmissionRequiresExactlyTheDigestTheAgentWasHanded(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())
	handed, found := SchemaDigestFor(syntheticRecordType)
	if !found {
		t.Fatalf("SchemaDigestFor(%q) not found", syntheticRecordType)
	}
	declarations := syntheticDeclarations(t)

	accepted, found := AcceptedSchemaDigests(syntheticRecordType)
	if !found || len(accepted) < 2 {
		t.Fatalf("AcceptedSchemaDigests(%q) = %v, want at least two revisions", syntheticRecordType, accepted)
	}
	for _, digest := range accepted {
		record := syntheticRecordPinning(t, digest)
		err := record.AdmitForSeal(syntheticRecordType, declarations)
		if digest == handed {
			if err != nil {
				t.Fatalf("AdmitForSeal() rejected the digest the agent was handed (%q): %v", digest, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("AdmitForSeal() accepted %q, which is not the digest the agent was handed (%q)", digest, handed)
		}
	}
}

// TestNewRecordCannotAuthorASupersededSchemaDigest closes the authoring door as
// well as the admission door: NewRecord always pins index 0, and its own
// envelope check must be the seal-time one.
func TestNewRecordCannotAuthorASupersededSchemaDigest(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())
	record, err := NewRecord(syntheticRecordType, syntheticSubjects(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("NewRecord(): %v", err)
	}
	if want := schemaDescriptorDigest(syntheticRevision2); record.Schema != want {
		t.Fatalf("NewRecord().Schema = %q, want the current digest %q", record.Schema, want)
	}
}

// TestSchemaAdmissionFailsClosedWhenNoGateIsStated pins the fail-closed zero
// value. A future envelope path that forgets to say which gate it is must be
// rejected, not silently handed the looser read-time rule.
func TestSchemaAdmissionFailsClosedWhenNoGateIsStated(t *testing.T) {
	withSyntheticSchemaHistories(t, syntheticTwoRevisionHistories())
	current := schemaDescriptorDigest(syntheticRevision2)
	if err := schemaAdmissionUnstated.admit(syntheticRecordType, current); err == nil {
		t.Fatal("the zero schemaAdmission admitted the current digest; it must admit nothing")
	}
	record := syntheticRecordPinning(t, current)
	if err := record.validateEnvelopeShape(syntheticRecordType, schemaAdmissionUnstated); err == nil {
		t.Fatal("validateEnvelopeShape() accepted a record without a stated gate")
	}
}

// TestEveryRecordTypeExposesBothGates is the audit as a test. Both gates must
// exist for every registered type — a validator that cannot re-validate a stored
// record makes that type's corpus unreadable — and the read gate must not need
// declarations that a reader has no way to reconstruct.
//
// It asserts reachability, not that an empty tree validates: an empty tree has no
// record.json, so what is checked is that RevalidateSealed fails on the MISSING
// DOCUMENT rather than on absent declarations.
func TestEveryRecordTypeExposesBothGates(t *testing.T) {
	registry, err := NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry(): %v", err)
	}
	empty, err := snapshot.NewValidationContext(nil, nil)
	if err != nil {
		t.Fatalf("NewValidationContext(): %v", err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatalf("OpenRoot(): %v", err)
	}
	defer root.Close()
	for _, ref := range registry.Types() {
		if !IsRecordType(ref) {
			continue
		}
		validator, err := registry.Lookup(ref)
		if err != nil {
			t.Fatalf("Lookup(%q): %v", ref, err)
		}
		_, sealErr := validator.AdmitForSeal(context.Background(), root, empty)
		if sealErr == nil {
			t.Fatalf("%q AdmitForSeal() accepted a tree with no record.json", ref)
		}
		_, readErr := validator.RevalidateSealed(context.Background(), root, empty)
		if readErr == nil {
			t.Fatalf("%q RevalidateSealed() accepted a tree with no record.json", ref)
		}
		// The read gate must fail because record.json is absent, never because a
		// declaration a reader cannot have was missing.
		if !strings.Contains(readErr.Error(), "record.json") {
			t.Fatalf("%q RevalidateSealed() with no declarations failed on %v; "+
				"a stored record must be re-validatable without live step declarations", ref, readErr)
		}
	}
}
