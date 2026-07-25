package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

// The generic core validator has to run at BOTH gates for every record type, and
// this is what proves it from the outside: each record below violates one rule of
// the enforced core, and the rejection is expected to name the field in the frozen
// declaration grammar at seal time AND at read time.
//
// Naming the path is what makes the test able to tell WHICH validator spoke. Every
// one of these violations is also caught by the per-type Go validator — the core is
// a subset, by design — so a test that only asserted "rejected" would pass with the
// generic validator removed entirely.
func TestTheDeclaredCoreRunsAtBothGatesForEveryRecordType(t *testing.T) {
	primary := snapshot.SnapshotRef{ID: 71, Type: mustTypeRef(t, "repository/v1"), Digest: recordDigest('a')}
	other := snapshot.SnapshotRef{ID: 72, Type: mustTypeRef(t, "repository/v1"), Digest: recordDigest('b')}

	subject := func(id string, role contracts.SubjectRole, input string, ref snapshot.SnapshotRef) contracts.Subject {
		return contracts.SubjectFromInput(id, role, input, ref)
	}
	anchor := func() contracts.Anchor {
		return contracts.Anchor{
			Subject: "primary",
			Locator: contracts.Locator{Kind: "opaque", Value: "one line of log"},
		}
	}

	for _, testCase := range []struct {
		rawType  string
		subjects []contracts.Subject
		body     any
		inputs   map[string]snapshot.SnapshotRef
		ports    []string
		wantPath string
	}{
		{
			rawType:  "review/v1",
			subjects: []contracts.Subject{subject("primary", contracts.SubjectRolePrimary, "primary", primary)},
			inputs:   map[string]snapshot.SnapshotRef{"primary": primary},
			// A blank summary: declared required, and for a string-shaped kind
			// required is the same statement as "rejects blank".
			body:     contracts.ReviewBody{Conclusion: "accept", Summary: "   "},
			wantPath: "body/summary",
		},
		{
			rawType:  "diagnosis/v1",
			subjects: []contracts.Subject{subject("primary", contracts.SubjectRolePrimary, "primary", primary)},
			inputs:   map[string]snapshot.SnapshotRef{"primary": primary},
			// A score scale outside the value the site pins it to.
			body: contracts.DiagnosisBody{
				Summary: "one hypothesis", Conclusion: "suspected",
				Hypotheses: []contracts.Hypothesis{{
					ID: "h-1", Rank: 1, Statement: "a guess",
					Confidence: contracts.Score{Value: 5, Scale: "bounded", Direction: "higher-is-better"},
				}},
			},
			wantPath: "body/hypotheses/*/confidence/scale",
		},
		{
			rawType:  "validation/v1",
			subjects: []contracts.Subject{subject("primary", contracts.SubjectRolePrimary, "primary", primary)},
			inputs:   map[string]snapshot.SnapshotRef{"primary": primary},
			// A check kind outside the declared enum.
			body: contracts.ValidationBody{
				Conclusion: "passed", Summary: "one check",
				Checks: []contracts.ValidationCheck{{
					ID: "c-1", Kind: "smoke", Name: "smoke", Status: "passed",
					Attempts: []contracts.ValidationAttempt{{Number: 1, Status: "passed", Duration: "1s"}},
				}},
			},
			wantPath: "body/checks/*/kind",
		},
		{
			rawType:  "repository-change/v1",
			subjects: []contracts.Subject{subject("base", contracts.SubjectRoleBase, "base", primary)},
			inputs:   map[string]snapshot.SnapshotRef{"base": primary},
			// A payload path outside the prefix the blob declares.
			body: contracts.RepositoryChangeBody{
				RepositoryID:   recordDigest('a').String(),
				BaseSHA:        strings.Repeat("a", 40),
				Representation: "patch",
				Payload: contracts.ContentRef{
					Path: "artifacts/change.patch", Digest: recordDigest('b'), MediaType: "text/x-patch",
				},
				ResultTree: strings.Repeat("b", 40),
			},
			wantPath: "body/payload/path",
		},
		{
			rawType: "selection/v1",
			subjects: []contracts.Subject{
				subject("left", contracts.SubjectRoleCandidate, "left", primary),
				subject("right", contracts.SubjectRoleCandidate, "right", other),
			},
			inputs: map[string]snapshot.SnapshotRef{"left": primary, "right": other},
			ports:  []string{"left", "right"},
			// Candidates out of lexicographic id order.
			body: contracts.SelectionBody{
				Selected: "left",
				Candidates: []contracts.CandidateAssessment{
					{ID: "right", Rank: 2, Summary: "second"},
					{ID: "left", Rank: 1, Summary: "first"},
				},
				Rationale: "left is closer",
			},
			wantPath: "body/candidates",
		},
		{
			rawType:  "measurements/v1",
			subjects: []contracts.Subject{subject("primary", contracts.SubjectRolePrimary, "primary", primary)},
			inputs:   map[string]snapshot.SnapshotRef{"primary": primary},
			// A metric direction outside the declared enum, on a metric that also
			// carries an anchor, so the record exercises both halves of the core.
			body: contracts.MeasurementsBody{
				Conclusion: "measured",
				Metrics: []contracts.Measurement{{
					ID: "m-1", Value: 3, Unit: "ms", Direction: "sideways",
					Evidence: []contracts.Anchor{anchor()},
				}},
			},
			wantPath: "body/metrics/*/direction",
		},
	} {
		t.Run(testCase.rawType, func(t *testing.T) {
			record, err := contracts.NewRecord(mustTypeRef(t, testCase.rawType), testCase.subjects, testCase.body)
			if err != nil {
				t.Fatalf("NewRecord(): %v", err)
			}
			files := map[string][]byte{"record.json": marshalRecord(t, record)}
			sealContext := validationContextFor(t, testCase.inputs)
			if len(testCase.ports) > 0 {
				sealContext = candidateValidationContextFor(t, testCase.inputs, testCase.ports...)
			}

			_, sealErr := validateFiles(t, testCase.rawType, files, sealContext)
			assertNamesDeclaredField(t, "seal-time admission", sealErr, testCase.wantPath)

			_, readErr := revalidateSealedFiles(t, testCase.rawType, files, sealContext)
			assertNamesDeclaredField(t, "read-time revalidation", readErr, testCase.wantPath)
		})
	}
}

func assertNamesDeclaredField(t *testing.T, gate string, err error, wantPath string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s accepted a record that violates the declared core", gate)
	}
	if !strings.Contains(err.Error(), wantPath+":") {
		t.Errorf("%s did not reject through the declared schema; the error names no declared field path\n  got: %v", gate, err)
	}
}
