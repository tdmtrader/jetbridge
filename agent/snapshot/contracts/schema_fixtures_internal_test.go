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

// The rev3 validation wire projection makes an explicit zero log size
// distinguishable from an omitted member. Schema fixtures must validate that
// projection just as the seal/read gate does.
func validateFixtureDeclared(document SchemaDocument, ref snapshot.TypeRef, subjects []Subject, body any) error {
	if ref == validationType {
		return document.validateDecodedRecord(subjects, validationDeclaredBody(body.(ValidationBody), 3))
	}
	return document.validateDecodedRecord(subjects, body)
}

func recordFixtures() []recordFixture {
	return concatFixtures(
		reviewFixtures(),
		diagnosisFixtures(),
		validationFixtures(),
		repositoryChangeFixtures(),
		measurementsFixtures(),
		pullRequestFixtures(),
		pullRequestResponseFixtures(),
		publishImpactFixtures(),
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
	supporting := fixtureSubjectSet(
		fixtureRoleCount{SubjectRolePrimary, 1}, fixtureRoleCount{SubjectRoleBase, 2},
		fixtureRoleCount{SubjectRoleEvidence, 2}, fixtureRoleCount{SubjectRoleContext, 2}, fixtureRoleCount{SubjectRoleReference, 2},
	)
	primaryOnly := fixtureSubjects(SubjectRolePrimary, "primary")
	attestation := func(subjects []Subject) ValidationAttestation {
		var primary Subject
		var bases []ValidationBaseInput
		for _, subject := range subjects {
			if subject.Role == SubjectRolePrimary {
				primary = subject
			}
			if subject.Role == SubjectRoleBase {
				bases = append(bases, ValidationBaseInput{Input: subject.Input, Type: subject.Type, Digest: subject.Digest})
			}
		}
		return ValidationAttestation{CandidateDigest: primary.Digest, BaseInputs: bases, ProfileDigest: fixtureDigest('a'), ProtectedConfigDigest: fixtureDigest('b'), CapabilityImage: "example.invalid/validator@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", CapabilityImageDigest: fixtureDigest('c'), WorkflowDefinitionID: 1, WorkflowVersion: 1, Toolchain: "dev-capability/v1"}
	}
	log := func(path string) ValidationLog {
		return ValidationLog{Path: path, Digest: fixtureDigest('d'), Size: 1, MediaType: "text/plain"}
	}
	return []recordFixture{
		{
			name: "validation/failed", ref: validationType, subjects: supporting, validate: validate,
			body: ValidationBody{
				Conclusion:  "failed",
				Summary:     "One suite fails, one check was skipped.",
				Attestation: attestation(supporting),
				Checks: []ValidationCheck{
					{
						ID: "c-1", Kind: "test", Name: "go test ./...", Status: "failed",
						Attempts: []ValidationAttempt{
							{Number: 1, Status: "failed", Duration: "1s", Log: ValidationLog{Path: "content/logs/c-1-1.log", Digest: fixtureDigest('d'), Size: 0, MediaType: "text/plain"}},
							{
								Number: 2, Status: "failed", Duration: "2s",
								Log: log("content/logs/c-1-2.log"), Evidence: []Anchor{fileLinesAnchor("primary-1"), opaqueAnchor("evidence-1"), jsonPointerAnchor("primary-1")},
								Detail: "same failure on retry",
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
			body: ValidationBody{Conclusion: "incomplete", Summary: "Nothing ran.", Attestation: attestation(primaryOnly)},
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

func pullRequestFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(PullRequestBody).Validate(subjects)
	}
	everyRole := fixtureSubjectSet(
		fixtureRoleCount{SubjectRolePrimary, 2}, fixtureRoleCount{SubjectRoleBase, 2},
		fixtureRoleCount{SubjectRoleEvidence, 2}, fixtureRoleCount{SubjectRoleContext, 2},
		fixtureRoleCount{SubjectRoleCandidate, 2}, fixtureRoleCount{SubjectRoleReference, 2},
	)
	rich := PullRequestBody{
		Provider: "github", Repository: "github.example/acme/widget", ExternalID: "42", URL: "https://github.example/acme/widget/pull/42",
		State: PullRequestActive, Mergeability: PullRequestMergeable, SourceRef: "refs/heads/agent/change", SourceSHA: strings.Repeat("a", 40),
		TargetRef: "refs/heads/main", TargetSHA: strings.Repeat("b", 40), Iteration: "iteration-1", Trigger: PullRequestReviewBatchTrigger,
		ReviewBatches: []PullRequestReviewBatch{{ID: "batch-1", ReviewID: "review-1", CommitSHA: strings.Repeat("a", 40), Reviewer: "reviewer-1", Ready: true, ThreadIDs: []string{"thread-1"}}},
		Threads: []PullRequestThread{{
			ID: "thread-1", Iteration: "iteration-1", Anchor: &PullRequestAnchor{Path: "main.go", StartLine: 12, EndLine: 18},
			Comments: []PullRequestComment{{ID: "comment-1", Author: "reviewer-1", Body: "Please revise this.", CommitSHA: strings.Repeat("a", 40)}},
		}},
	}
	simple := rich
	simple.ReviewBatches = []PullRequestReviewBatch{{ID: "batch-1", ReviewID: "review-1", CommitSHA: strings.Repeat("a", 40), Reviewer: "reviewer-1", Ready: true}}
	simple.Threads = []PullRequestThread{{ID: "thread-1", Iteration: "iteration-1"}}
	missing := PullRequestBody{
		Provider: "github", Repository: "github.example/acme/widget", State: PullRequestMissing, Mergeability: PullRequestUnknown,
		SourceRef: "refs/heads/agent/change", ExpectedSource: &PullRequestHeadExpectation{Exists: true, SHA: strings.Repeat("a", 40)},
		TargetRef: "refs/heads/main", TargetSHA: strings.Repeat("b", 40), Iteration: "initial-1", Trigger: PullRequestInitialPublishTrigger,
	}
	missingAbsentSource := missing
	missingAbsentSource.ExpectedSource = &PullRequestHeadExpectation{Exists: false}
	return []recordFixture{
		{name: "pull-request/rich-active", ref: pullRequestType, subjects: everyRole, body: rich, validate: validate},
		{name: "pull-request/simple-active", ref: pullRequestType, body: simple, validate: validate},
		{name: "pull-request/missing", ref: pullRequestType, body: missing, validate: validate},
		{name: "pull-request/missing-absent-source", ref: pullRequestType, body: missingAbsentSource, validate: validate},
	}
}

func pullRequestResponseFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		if err := validatePullRequestResponseSubjects(subjects); err != nil {
			return err
		}
		return body.(PullRequestResponseBody).Validate(subjects)
	}
	subjects := fixtureSubjects(SubjectRolePrimary, "primary")
	subjects[0].Type = pullRequestType
	return []recordFixture{
		{name: "pull-request-response/reply", ref: pullRequestResponseType, subjects: subjects, validate: validate,
			body: PullRequestResponseBody{BatchID: "batch-1", Summary: "Addressed the review.", Replies: []PullRequestThreadResponse{{ThreadID: "thread-1", Body: "Updated in the latest revision."}}}},
		{name: "pull-request-response/summary", ref: pullRequestResponseType, subjects: subjects, validate: validate,
			body: PullRequestResponseBody{BatchID: "batch-1", Summary: "No thread-level response is needed."}},
	}
}

func publishImpactFixtures() []recordFixture {
	validate := func(subjects []Subject, body any) error {
		return body.(PublishImpactBody).Validate(subjects)
	}
	everyRole := fixtureSubjectSet(
		fixtureRoleCount{SubjectRolePrimary, 2}, fixtureRoleCount{SubjectRoleBase, 2},
		fixtureRoleCount{SubjectRoleEvidence, 2}, fixtureRoleCount{SubjectRoleContext, 2},
		fixtureRoleCount{SubjectRoleCandidate, 2}, fixtureRoleCount{SubjectRoleReference, 2},
	)
	return []recordFixture{
		{name: "publish-impact/detailed", ref: publishImpactType, subjects: everyRole, validate: validate,
			body: PublishImpactBody{
				BaselineDigest: fixtureDigest('a').String(), CandidateDigest: fixtureDigest('b').String(),
				ChangedFiles: []PublishChangedFile{{Path: "main.go", AddedLines: 2, RemovedLines: 1}, {Path: "no-lines", AddedLines: 0, RemovedLines: 0}}, ChangedLines: 3, ConflictResolution: true,
				ValidationChanges:  []string{"Lint output changed.", "Test output changed."},
				RuleResults:        []PublishImpactRule{{ID: "rule-1", Passed: false, Reason: "Deterministic policy requires reapproval."}},
				AgentAssessment:    &AgentImpactAssessment{ReapprovalRequired: false, Rationale: "The deterministic requirement remains authoritative."},
				ReapprovalRequired: true, Reasons: []string{"Deterministic policy requirement."},
			}},
		{name: "publish-impact/agent-escalation", ref: publishImpactType, validate: validate,
			body: PublishImpactBody{BaselineDigest: fixtureDigest('a').String(), CandidateDigest: fixtureDigest('b').String(),
				RuleResults:        []PublishImpactRule{{ID: "rule-1", Passed: true, Reason: "No deterministic policy matched."}},
				AgentAssessment:    &AgentImpactAssessment{ReapprovalRequired: true, Rationale: "The semantic impact is significant."},
				ReapprovalRequired: true, Reasons: []string{"Agent escalation."}}},
		{name: "publish-impact/empty-delta", ref: publishImpactType, validate: validate,
			body: PublishImpactBody{BaselineDigest: fixtureDigest('a').String(), CandidateDigest: fixtureDigest('b').String()}},
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
			if err := validateFixtureDeclared(document, fixture.ref, fixture.subjects, fixture.body); err != nil {
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
