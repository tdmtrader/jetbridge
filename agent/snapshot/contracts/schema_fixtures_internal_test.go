package contracts

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
)

// The instances the parity gate drives through both descriptions of the contract.
//
// They are hand-written rather than generated from the declaration, on purpose:
// generating them from the schema would make the schema its own oracle, and the
// whole point of the gate is that two independent descriptions have to agree.
//
// The set is chosen to satisfy one demanding property, which §5.1 of the dialect
// requires and TestDeclaredOptionalFieldsHaveWitnessesBothWays enforces: for every
// field declared `optional`, some accepted instance must carry its absent image and
// some accepted instance must carry a real value. A field with no absence witness
// is not optional — it is `required` and mis-declared — so the fixtures are where
// that claim is actually paid for, and adding a document field means adding the
// instances that derive its presence.
type recordFixture struct {
	name     string
	ref      snapshot.TypeRef
	subjects []Subject
	body     any

	// validate is the per-type Go body validator at the READ-TIME gate, which is
	// the only gate a fixture can drive: seal-time admission needs live step
	// declarations, and a fixture has none.
	validate func(subjects []Subject, body any) error
}

func recordFixtures() []recordFixture {
	return concatFixtures(
		reviewFixtures(),
		diagnosisFixtures(),
		validationFixtures(),
		repositoryChangeFixtures(),
		selectionFixtures(),
		measurementsFixtures(),
	)
}

func concatFixtures(groups ...[]recordFixture) []recordFixture {
	var all []recordFixture
	for _, group := range groups {
		all = append(all, group...)
	}
	return all
}

// fixtureRoleCount is one entry of a subject set's declared shape, spelled as a
// slice rather than a map so generated ids and digests are deterministic.
type fixtureRoleCount struct {
	role  SubjectRole
	count int
}

// fixtureSubjectSet builds a sorted, unique subject set with the given per-role
// counts, every subject sharing one snapshot type so a cardinality fixture never
// trips the uniformity rule by accident.
//
// It exists because subject-shape cardinality is derivable only from instances
// that actually carry the counts a falsified bound would let through. A corpus
// where every review carries exactly one `reference` subject cannot tell an
// unbounded maximum from a maximum of one, in either direction:
// TestFixturesWitnessEveryDeclaredSubjectCardinality is what forces the corpus to
// carry them, and TestEveryFixtureIsAcceptedByBothDescriptions is what then turns
// a falsified bound into a failure.
func fixtureSubjectSet(counts ...fixtureRoleCount) []Subject {
	var subjects []Subject
	for _, entry := range counts {
		for index := 1; index <= entry.count; index++ {
			id := fmt.Sprintf("%s-%d", entry.role, index)
			subjects = append(subjects, Subject{
				ID: id, Role: entry.role, Input: "input-" + id, Type: "repository/v1",
			})
		}
	}
	sort.Slice(subjects, func(left, right int) bool { return subjects[left].ID < subjects[right].ID })
	for index := range subjects {
		subjects[index].Digest = fixtureDigestAt(index)
	}
	return subjects
}

// supportingSubjectSet is the shape the four `onePrimaryWithSupportingSubjects`
// types share: exactly one primary, and two of every supporting role — two rather
// than one because a role whose declared maximum is unbounded needs an accepted
// instance carrying more than any plausible falsified bound of one.
func supportingSubjectSet() []Subject {
	return fixtureSubjectSet(
		fixtureRoleCount{SubjectRolePrimary, 1},
		fixtureRoleCount{SubjectRoleEvidence, 2},
		fixtureRoleCount{SubjectRoleContext, 2},
		fixtureRoleCount{SubjectRoleReference, 2},
	)
}

func fixtureSubjects(role SubjectRole, ids ...string) []Subject {
	subjects := make([]Subject, 0, len(ids))
	for index, id := range ids {
		subjects = append(subjects, Subject{
			ID:     id,
			Role:   role,
			Input:  "input-" + id,
			Type:   "repository/v1",
			Digest: fixtureDigest(byte('a' + index)),
		})
	}
	return subjects
}

func fixtureDigest(fill byte) snapshot.Digest {
	return snapshot.Digest("sha256:" + strings.Repeat(string(fill), 64))
}

// fixtureDigestAt keeps generated digests inside the sha256 alphabet. A digest is
// validated wherever a fixture meets a ValidationContext, so a subject set large
// enough to run past 'f' would fail on its spelling rather than on the property
// under test.
func fixtureDigestAt(index int) snapshot.Digest {
	const hexDigits = "0123456789abcdef"
	return fixtureDigest(hexDigits[index%len(hexDigits)])
}

func fileLinesAnchor(subject string) Anchor {
	start, end := 12, 18
	return Anchor{
		Subject: subject,
		Locator: Locator{Kind: "file-lines", Path: "main.go", Start: &start, End: &end},
	}
}

// opaqueAnchor is not decoration. It is the absence witness for locator/path,
// locator/start and locator/end, and the presence witness for locator/value,
// exactly as the file-lines anchor is the witness the other way round. The three
// anchor kinds together are what make every locator leaf's declared presence
// derivable rather than asserted.
func opaqueAnchor(subject string) Anchor {
	return Anchor{Subject: subject, Locator: Locator{Kind: "opaque", Value: "build log line 44"}}
}

func jsonPointerAnchor(subject string) Anchor {
	return Anchor{Subject: subject, Locator: Locator{Kind: "json-pointer", Pointer: "/checks/0/status"}}
}

func reviewFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(ReviewBody).Validate(subjects)
	}
	// Two subject sets, and the difference between them is the point: the first
	// carries two of every supporting role, the second carries none of them. One
	// witnesses that those roles are unbounded above, the other that they are
	// unbounded below, and a falsified bound in either direction stops being an
	// accepted instance.
	supporting := supportingSubjectSet()
	primaryOnly := fixtureSubjects(SubjectRolePrimary, "primary")
	return []recordFixture{
		{
			name: "review/changes-required", ref: reviewType, subjects: supporting, validate: validate,
			body: ReviewBody{
				Conclusion: "changes-required",
				Summary:    "One blocking defect and one note.",
				Findings: []Finding{
					{
						ID: "f-1", Severity: "high", Blocking: true,
						Category: "correctness", Title: "unsafe race", Description: "Concurrent writes race.",
						Evidence:       []Anchor{fileLinesAnchor("primary-1"), opaqueAnchor("reference-1"), jsonPointerAnchor("evidence-2")},
						Recommendation: "Synchronize the writes.",
					},
					{
						ID: "f-2", Severity: "observation",
						Category: "style", Title: "naming", Description: "Prefer a fuller name.",
					},
				},
			},
		},
		{
			name: "review/accept-with-no-findings", ref: reviewType, subjects: primaryOnly, validate: validate,
			body: ReviewBody{Conclusion: "accept", Summary: "Nothing to change."},
		},
		{
			name: "review/accept-with-an-observation", ref: reviewType, subjects: primaryOnly, validate: validate,
			body: ReviewBody{
				Conclusion: "accept",
				Summary:    "One note, nothing blocking.",
				Findings: []Finding{{
					ID: "f-1", Severity: "observation",
					Category: "style", Title: "naming", Description: "Prefer a fuller name.",
				}},
			},
		},
	}
}

func diagnosisFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(DiagnosisBody).Validate(subjects)
	}
	supporting := supportingSubjectSet()
	primaryOnly := fixtureSubjects(SubjectRolePrimary, "primary")
	confidence := func(value float64) Score {
		return Score{Value: value, Scale: "unit-interval", Direction: "higher-is-better"}
	}
	return []recordFixture{
		{
			name: "diagnosis/identified", ref: diagnosisType, subjects: supporting, validate: validate,
			body: DiagnosisBody{
				Summary:    "The lock is taken twice on one path.",
				Conclusion: "identified",
				Hypotheses: []Hypothesis{
					{
						ID: "h-1", Rank: 1, Statement: "Re-entrant lock acquisition deadlocks.",
						Confidence: confidence(0.9),
						Evidence:   []Anchor{fileLinesAnchor("primary-1"), opaqueAnchor("context-1"), jsonPointerAnchor("primary-1")},
						// All three locator kinds again: counterevidence declares its own
						// anchor leaves, so it needs its own witnesses. Reusing the
						// evidence array's would be witnessing a different declaration.
						Counterevidence: []Anchor{fileLinesAnchor("primary-1"), opaqueAnchor("reference-2"), jsonPointerAnchor("primary-1")},
					},
					{
						ID: "h-2", Rank: 2, Statement: "The timeout is too short.",
						Confidence: confidence(0),
					},
				},
				Actions: []DiagnosisAction{
					{
						ID: "a-1", Priority: "immediate", Description: "Make the lock re-entrant.",
						// Two addresses, sorted: an array of a scalar kind is a leaf the
						// grammar cannot address an element of, so its unique and sorted
						// rules are only reachable with more than one entry.
						Addresses: []string{"h-1", "h-2"}, Rationale: "Removes the deadlock outright.",
					},
					{ID: "a-2", Priority: "next", Description: "Add a lock-order test."},
				},
			},
		},
		{
			name: "diagnosis/inconclusive", ref: diagnosisType, subjects: primaryOnly, validate: validate,
			body: DiagnosisBody{Summary: "Not reproducible.", Conclusion: "inconclusive"},
		},
	}
}

func validationFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(ValidationBody).Validate(subjects)
	}
	supporting := supportingSubjectSet()
	primaryOnly := fixtureSubjects(SubjectRolePrimary, "primary")
	return []recordFixture{
		{
			name: "validation/failed", ref: validationType, subjects: supporting, validate: validate,
			body: ValidationBody{
				Conclusion: "failed",
				Summary:    "One suite fails, one check was skipped.",
				Checks: []ValidationCheck{
					{
						ID: "c-1", Kind: "test", Name: "go test ./...", Status: "failed",
						Attempts: []ValidationAttempt{
							{Number: 1, Status: "failed", Duration: "1s"},
							{
								Number: 2, Status: "failed", Duration: "2s",
								Evidence: []Anchor{fileLinesAnchor("primary-1"), opaqueAnchor("evidence-1"), jsonPointerAnchor("primary-1")},
								Detail:   "same failure on retry",
							},
						},
						Detail: "flaky suspected, retried once",
					},
					{ID: "c-2", Kind: "lint", Name: "golangci-lint", Status: "skipped"},
				},
			},
		},
		{
			name: "validation/incomplete-with-no-checks", ref: validationType, subjects: primaryOnly, validate: validate,
			body: ValidationBody{Conclusion: "incomplete", Summary: "Nothing ran."},
		},
	}
}

func repositoryChangeFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(RepositoryChangeBody).Validate(subjects)
	}
	subjects := fixtureSubjects(SubjectRoleBase, "base")
	payload := ContentRef{
		Path:      "content/change.patch",
		Digest:    fixtureDigest('b'),
		MediaType: "text/x-patch",
	}
	return []recordFixture{
		{
			name: "repository-change/patch", ref: repositoryChangeType, subjects: subjects, validate: validate,
			body: RepositoryChangeBody{
				RepositoryID:   fixtureDigest('a').String(),
				BaseSHA:        strings.Repeat("a", 40),
				Representation: "patch",
				Payload:        payload,
				ResultTree:     strings.Repeat("b", 40),
			},
		},
		{
			name: "repository-change/git-tree", ref: repositoryChangeType, subjects: subjects, validate: validate,
			body: RepositoryChangeBody{
				RepositoryID:   fixtureDigest('a').String(),
				BaseSHA:        strings.Repeat("a", 40),
				Representation: "git-tree",
				Payload:        ContentRef{Path: "content/tree.tar", Digest: fixtureDigest('c'), MediaType: "application/x-tar"},
				ResultTree:     strings.Repeat("b", 40),
				ResultCommit:   strings.Repeat("c", 40),
			},
		},
	}
}

func selectionFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(SelectionBody).RevalidateSealed(subjects)
	}
	subjects := fixtureSubjects(SubjectRoleCandidate, "left", "right")
	minimum, maximum, target := 0.0, 10.0, 7.0
	return []recordFixture{
		{
			name: "selection/two-candidates", ref: selectionType, subjects: subjects, validate: validate,
			body: SelectionBody{
				Selected: "right",
				Candidates: []CandidateAssessment{
					{
						ID: "left", Rank: 2, Summary: "Viable but slower.",
						Scores: []NamedScore{
							// Value 0 is the absence witness for a score's value: the absent
							// image of a float64 IS 0, so no rule can distinguish this from an
							// omitted value, which is exactly why value is declared optional.
							{ID: "s-1", Score: Score{Scale: "unit-interval", Direction: "higher-is-better"}},
							{ID: "s-2", Score: Score{
								Value: 5, Scale: "bounded", Direction: "target",
								Minimum: &minimum, Maximum: &maximum, Target: &target,
							}},
						},
					},
					{ID: "right", Rank: 1, Summary: "Strongest evidence."},
				},
				Rationale: "Right wins on every dimension that matters.",
			},
		},
		{
			// A judge given one surviving candidate still has to say so. This is
			// the instance that sits exactly on the declared minimum of one, and
			// without it raising that minimum would fail nothing.
			name: "selection/one-candidate", ref: selectionType, validate: validate,
			subjects: fixtureSubjects(SubjectRoleCandidate, "only"),
			body: SelectionBody{
				Selected:   "only",
				Candidates: []CandidateAssessment{{ID: "only", Rank: 1, Summary: "The only survivor."}},
				Rationale:  "Nothing else reached the judge.",
			},
		},
	}
}

func measurementsFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(MeasurementsBody).Validate(subjects)
	}
	// measurements/v1 allows every role, unbounded, and requires none of them.
	// Declaring that faithfully is only worth anything if the corpus proves it, so
	// one instance carries two subjects in all six roles and one carries no
	// subjects at all — the record the dialect calls out as the least expected fact
	// in the six documents, and the only witness that the set minimum really is 0.
	everyRole := fixtureSubjectSet(
		fixtureRoleCount{SubjectRolePrimary, 2},
		fixtureRoleCount{SubjectRoleBase, 2},
		fixtureRoleCount{SubjectRoleEvidence, 2},
		fixtureRoleCount{SubjectRoleContext, 2},
		fixtureRoleCount{SubjectRoleCandidate, 2},
		fixtureRoleCount{SubjectRoleReference, 2},
	)
	minimum, maximum, target := 0.0, 10.0, 5.0
	return []recordFixture{
		{
			name: "measurements/measured", ref: measurementsType, subjects: everyRole, validate: validate,
			body: MeasurementsBody{
				Conclusion: "measured",
				Metrics: []Measurement{
					{
						ID: "m-1", Value: 1.5, Unit: "ms", Direction: "lower-is-better",
						Evidence: []Anchor{fileLinesAnchor("primary-1"), opaqueAnchor("candidate-2"), jsonPointerAnchor("base-1")},
					},
					{
						ID: "m-2", Value: 5, Unit: "count", Direction: "target",
						Minimum: &minimum, Maximum: &maximum, Target: &target,
					},
					// A measured zero, indistinguishable from an omitted value, which is
					// why body/metrics/*/value is optional despite being the point of the
					// record.
					{ID: "m-3", Unit: "count", Direction: "higher-is-better"},
				},
			},
		},
		{
			name: "measurements/not-applicable", ref: measurementsType,
			subjects: fixtureSubjects(SubjectRolePrimary, "primary"), validate: validate,
			body: MeasurementsBody{
				Conclusion:  "not-applicable",
				Explanation: "The workload does not apply to this change.",
			},
		},
		{
			// No subjects, no metrics, no evidence. The dialect declares this
			// accepted because the Go validator accepts it, not because anyone
			// wants it: recording the tolerance truthfully is what makes closing
			// it a data migration rather than a silent schema tightening.
			name: "measurements/not-applicable-with-no-subjects", ref: measurementsType,
			subjects: nil, validate: validate,
			body: MeasurementsBody{
				Conclusion:  "not-applicable",
				Explanation: "Nothing was exposed to measure.",
			},
		},
	}
}

// Both descriptions must accept every fixture. If this fails, nothing downstream
// of it means anything: a mutation test over an instance that was already invalid
// proves only that two validators agree about garbage.
func TestEveryFixtureIsAcceptedByBothDescriptions(t *testing.T) {
	for _, fixture := range recordFixtures() {
		t.Run(fixture.name, func(t *testing.T) {
			document, found := recordSchemaDocuments[fixture.ref]
			if !found {
				t.Fatalf("%q has no schema document", fixture.ref)
			}
			if err := document.validateDecodedRecord(fixture.subjects, fixture.body); err != nil {
				t.Errorf("the declared schema rejected a valid instance: %v", err)
			}
			if err := fixture.validate(fixture.subjects, fixture.body); err != nil {
				t.Errorf("the Go validator rejected a valid instance: %v", err)
			}
		})
	}
}

// Every record type needs at least one fixture, or the parity gate silently covers
// five types out of six.
func TestEveryRecordTypeHasAFixture(t *testing.T) {
	covered := make(map[snapshot.TypeRef]int)
	for _, fixture := range recordFixtures() {
		covered[fixture.ref]++
	}
	for ref := range recordSchemaDocuments {
		if covered[ref] == 0 {
			t.Errorf("%q has no parity fixture", ref)
		}
	}
}

// TestFixturesWitnessEveryDeclaredSubjectCardinality is the assertion that turns
// subject_shape from a claim into a derivation.
//
// Every other test in this file compares the two descriptions over the instances
// the corpus happens to carry. A cardinality bound is different: it is invisible
// unless an accepted instance sits exactly ON it. A corpus where every review
// carries one `reference` subject cannot distinguish `"maximum": null` from
// `"maximum": 1`, and the parity gate stayed green while that bound was falsified
// in either direction — the corpus-corrupting one included.
//
// So the corpus must witness each bound, and then
// TestEveryFixtureIsAcceptedByBothDescriptions does the detecting for free:
//
//   - an unbounded maximum needs an instance carrying at least two, or lowering it
//     to one changes nothing;
//   - a finite maximum M needs an instance carrying exactly M, or lowering it to
//     M-1 changes nothing;
//   - a minimum of zero needs an instance carrying none, or raising it to one
//     changes nothing.
//
// Each witness is load-bearing in BOTH directions at once: the same instance that
// makes an over-declared bound reject a valid record is the one that makes a
// Go validator tightened behind the schema's back reject a declared-valid record.
func TestFixturesWitnessEveryDeclaredSubjectCardinality(t *testing.T) {
	fixtures := recordFixtures()
	for ref, document := range recordSchemaDocuments {
		shape := document.SubjectShape
		counts := func(count func([]Subject) int) []int {
			var observed []int
			for _, fixture := range fixtures {
				if fixture.ref == ref {
					observed = append(observed, count(fixture.subjects))
				}
			}
			return observed
		}
		assertBoundIsWitnessed(t, ref.String(), "the subject set", shape.Minimum, shape.Maximum, counts(func(subjects []Subject) int {
			return len(subjects)
		}))
		for _, role := range sortedRoles(shape.Roles) {
			bounds := shape.Roles[role]
			assertBoundIsWitnessed(t, ref.String(), fmt.Sprintf("the %q role", role), bounds.Minimum, bounds.Maximum, counts(func(subjects []Subject) int {
				total := 0
				for _, subject := range subjects {
					if subject.Role == role {
						total++
					}
				}
				return total
			}))
		}
	}
}

func assertBoundIsWitnessed(t *testing.T, ref, what string, minimum int, maximum *int, observed []int) {
	t.Helper()
	carries := func(want func(int) bool) bool {
		for _, count := range observed {
			if want(count) {
				return true
			}
		}
		return false
	}
	if minimum == 0 && !carries(func(count int) bool { return count == 0 }) {
		t.Errorf(
			"%s declares a minimum of 0 for %s but no accepted instance carries none of them, so raising that minimum would fail nothing; observed counts %v",
			ref, what, observed,
		)
	}
	if minimum > 0 && !carries(func(count int) bool { return count == minimum }) {
		t.Errorf(
			"%s declares a minimum of %d for %s but no accepted instance carries exactly that many, so raising the minimum would fail nothing; observed counts %v",
			ref, minimum, what, observed,
		)
	}
	if maximum == nil {
		if !carries(func(count int) bool { return count >= 2 }) {
			t.Errorf(
				"%s declares %s unbounded above but no accepted instance carries more than one, so lowering the maximum to one would fail nothing; observed counts %v",
				ref, what, observed,
			)
		}
		return
	}
	if !carries(func(count int) bool { return count == *maximum }) {
		t.Errorf(
			"%s declares a maximum of %d for %s but no accepted instance carries exactly that many, so lowering the maximum would fail nothing; observed counts %v",
			ref, *maximum, what, observed,
		)
	}
}
