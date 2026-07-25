package contracts_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestSelectionRecordResolvesAnExistingExactCandidate(t *testing.T) {
	typeRef := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 11, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("a", 64))}
	right := snapshot.SnapshotRef{ID: 12, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("b", 64))}
	validationContext := candidateValidationContextFor(
		t, map[string]snapshot.SnapshotRef{"left": left, "right": right}, "left", "right",
	)

	record, err := contracts.NewRecord(
		mustTypeRef(t, "selection/v1"),
		[]contracts.Subject{
			contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
			contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
		},
		contracts.SelectionBody{
			Selected: "right",
			Candidates: []contracts.CandidateAssessment{
				{ID: "left", Rank: 2, Summary: "viable"},
				{ID: "right", Rank: 1, Summary: "best"},
			},
			Rationale: "Right has the strongest evidence.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFiles(t, "selection/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, validationContext); err != nil {
		t.Fatalf("valid selection: %v", err)
	}
	selected, err := contracts.ResolveSelectionForSeal(record, validationContext)
	if err != nil {
		t.Fatalf("ResolveSelectionForSeal(): %v", err)
	}
	if selected != right {
		t.Fatalf("selected = %+v, want %+v", selected, right)
	}
}

func TestSelectionRecordRequiresEveryExactCandidateAndStableRanks(t *testing.T) {
	typeRef := mustTypeRef(t, "review/v1")
	left := snapshot.SnapshotRef{ID: 21, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("c", 64))}
	right := snapshot.SnapshotRef{ID: 22, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("d", 64))}
	validationContext := candidateValidationContextFor(
		t, map[string]snapshot.SnapshotRef{"left": left, "right": right}, "left", "right",
	)
	record, err := contracts.NewRecord(
		mustTypeRef(t, "selection/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left)},
		contracts.SelectionBody{
			Selected:   "left",
			Candidates: []contracts.CandidateAssessment{{ID: "left", Rank: 2, Summary: "only"}},
			Rationale:  "incomplete candidate set",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFiles(t, "selection/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, validationContext); err == nil || (!strings.Contains(err.Error(), "candidate port") && !strings.Contains(err.Error(), "rank")) {
		t.Fatalf("validation error = %v, want exact candidate or rank failure", err)
	}
}

// TestSelectionRecordSealsDeclaredCandidatesBesideContextInputs pins the target
// model: a judge is exposed to the surviving candidates AND to the material it
// judges them against. Only the declared candidate ports are subjects.
func TestSelectionRecordSealsDeclaredCandidatesBesideContextInputs(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 31, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("1", 64))}
	right := snapshot.SnapshotRef{ID: 32, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("2", 64))}
	base := snapshot.SnapshotRef{
		ID: 33, Type: mustTypeRef(t, "repository/v1"), Digest: mustDigest(t, "sha256:"+strings.Repeat("3", 64)),
	}
	validationContext := candidateValidationContextFor(t, map[string]snapshot.SnapshotRef{
		"left": left, "right": right, "base": base,
	}, "left", "right")

	record, err := contracts.NewRecord(
		mustTypeRef(t, "selection/v1"),
		[]contracts.Subject{
			contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
			contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
		},
		contracts.SelectionBody{
			Selected: "left",
			Candidates: []contracts.CandidateAssessment{
				{ID: "left", Rank: 1, Summary: "matches the base repository conventions"},
				{ID: "right", Rank: 2, Summary: "diverges from the base repository"},
			},
			Rationale: "Left is closest to the exposed base repository.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateFiles(t, "selection/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, validationContext); err != nil {
		t.Fatalf("selection with a non-candidate context input: %v", err)
	}
	selected, err := contracts.ResolveSelectionForSeal(record, validationContext)
	if err != nil {
		t.Fatalf("ResolveSelectionForSeal(): %v", err)
	}
	if selected != left {
		t.Fatalf("selected = %+v, want %+v", selected, left)
	}
}

func TestSelectionRecordRejectsSubjectsOutsideTheDeclaredCandidatePorts(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 41, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("4", 64))}
	right := snapshot.SnapshotRef{ID: 42, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("5", 64))}
	base := snapshot.SnapshotRef{
		ID: 43, Type: mustTypeRef(t, "repository/v1"), Digest: mustDigest(t, "sha256:"+strings.Repeat("6", 64)),
	}
	inputs := map[string]snapshot.SnapshotRef{"left": left, "right": right, "base": base}

	for name, testCase := range map[string]struct {
		subjects   []contracts.Subject
		body       contracts.SelectionBody
		wantErrors []string
	}{
		"selects an input that was never exposed": {
			subjects: []contracts.Subject{
				contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
				contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
			},
			body: contracts.SelectionBody{
				Selected: "ghost",
				Candidates: []contracts.CandidateAssessment{
					{ID: "ghost", Rank: 1, Summary: "never exposed"},
					{ID: "left", Rank: 2, Summary: "exposed"},
					{ID: "right", Rank: 3, Summary: "exposed"},
				},
				Rationale: "selecting something that was never exposed",
			},
			wantErrors: []string{"is not a declared candidate subject"},
		},
		"selects a non-candidate context input": {
			subjects: []contracts.Subject{
				contracts.SubjectFromInput("base", contracts.SubjectRoleCandidate, "base", base),
				contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
				contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
			},
			body: contracts.SelectionBody{
				Selected: "base",
				Candidates: []contracts.CandidateAssessment{
					{ID: "base", Rank: 1, Summary: "context masquerading as a candidate"},
					{ID: "left", Rank: 2, Summary: "exposed"},
					{ID: "right", Rank: 3, Summary: "exposed"},
				},
				Rationale: "selecting an input that is not a candidate port",
			},
			wantErrors: []string{"is not a declared candidate port"},
		},
		"omits a declared candidate port": {
			subjects: []contracts.Subject{
				contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
			},
			body: contracts.SelectionBody{
				Selected:   "left",
				Candidates: []contracts.CandidateAssessment{{ID: "left", Rank: 1, Summary: "the only assessed candidate"}},
				Rationale:  "the right candidate port was never assessed",
			},
			wantErrors: []string{"candidate port \"right\""},
		},
		"declares a subject that is not an exposed input": {
			subjects: []contracts.Subject{
				contracts.SubjectFromInput("ghost", contracts.SubjectRoleCandidate, "ghost", left),
				contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
				contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
			},
			body: contracts.SelectionBody{
				Selected: "left",
				Candidates: []contracts.CandidateAssessment{
					{ID: "ghost", Rank: 3, Summary: "unexposed"},
					{ID: "left", Rank: 1, Summary: "exposed"},
					{ID: "right", Rank: 2, Summary: "exposed"},
				},
				Rationale: "a subject bound to an input that does not exist",
			},
			wantErrors: []string{"is not an exact declared input"},
		},
		// This case is rejected by the record ENVELOPE gate, not by the selection
		// body validator: subject.Input uniqueness is an invariant of every record
		// type. The expectation names the exact envelope message so the subtest
		// cannot silently start claiming to cover a selection-specific rule that
		// does not exist. TestSelectionDuplicateCandidatePortIsRejectedByTheEnvelopeGate
		// asserts the layering directly.
		"claims one candidate port twice": {
			subjects: []contracts.Subject{
				contracts.SubjectFromInput("left-again", contracts.SubjectRoleCandidate, "left", left),
				contracts.SubjectFromInput("left-once", contracts.SubjectRoleCandidate, "left", left),
				contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
			},
			body: contracts.SelectionBody{
				Selected: "left-once",
				Candidates: []contracts.CandidateAssessment{
					{ID: "left-again", Rank: 2, Summary: "same port"},
					{ID: "left-once", Rank: 1, Summary: "same port"},
					{ID: "right", Rank: 3, Summary: "other port"},
				},
				Rationale: "two subjects claim the left candidate port",
			},
			wantErrors: []string{`subjects[1].input "left" is duplicate`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			validationContext := candidateValidationContextFor(t, inputs, "left", "right")
			// The envelope shape is checked on decode as well as on validate, so
			// some of these records cannot be constructed at all; that is a
			// rejection too.
			record, err := contracts.NewRecord(
				mustTypeRef(t, "selection/v1"), testCase.subjects, testCase.body,
			)
			if err != nil {
				assertContainsOneOf(t, err, testCase.wantErrors)
				return
			}
			_, err = validateFiles(t, "selection/v1", map[string][]byte{
				"record.json": marshalRecord(t, record),
			}, validationContext)
			if err == nil {
				t.Fatalf("validation succeeded, want one of %q", testCase.wantErrors)
			}
			assertContainsOneOf(t, err, testCase.wantErrors)
		})
	}
}

// TestSelectionDuplicateCandidatePortIsRejectedByTheEnvelopeGate pins WHERE the
// one-subject-per-candidate-port rule lives.
//
// Both of SelectionBody.validate's callers run the record envelope gate first,
// and that gate already rejects a duplicate subject.Input for every record type.
// So the selection body validator must not carry a second duplicate check: it
// would be unreachable, and an unreachable branch that a subtest appears to cover
// is worse than no branch at all. If a future change makes the body validator
// callable without the envelope gate, this test is the place the rule has to move
// back to — it fails here first.
func TestSelectionDuplicateCandidatePortIsRejectedByTheEnvelopeGate(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 41, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("4", 64))}
	right := snapshot.SnapshotRef{ID: 42, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("5", 64))}

	subjects := []contracts.Subject{
		contracts.SubjectFromInput("left-once", contracts.SubjectRoleCandidate, "left", left),
		contracts.SubjectFromInput("left-again", contracts.SubjectRoleCandidate, "left", left),
		contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
	}
	body := contracts.SelectionBody{
		Selected: "left-once",
		Candidates: []contracts.CandidateAssessment{
			{ID: "left-once", Rank: 1, Summary: "same port"},
			{ID: "left-again", Rank: 2, Summary: "same port"},
			{ID: "right", Rank: 3, Summary: "other port"},
		},
		Rationale: "two subjects claim the left candidate port",
	}

	_, err := contracts.NewRecord(mustTypeRef(t, "selection/v1"), subjects, body)
	if err == nil {
		t.Fatal("NewRecord accepted two subjects on one candidate port, want the envelope gate to reject it")
	}
	if !strings.Contains(err.Error(), `subjects[1].input "left" is duplicate`) {
		t.Fatalf("NewRecord error = %v, want the envelope duplicate-input rejection", err)
	}

	// The body validator is reached only for envelope-valid subjects, so it must
	// accept a unique-port subject set that differs from the above only in the
	// duplicate. This is what makes the removed branch provably redundant rather
	// than merely unexercised.
	unique := []contracts.Subject{
		contracts.SubjectFromInput("left-once", contracts.SubjectRoleCandidate, "left", left),
		contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
	}
	uniqueBody := contracts.SelectionBody{
		Selected: "left-once",
		Candidates: []contracts.CandidateAssessment{
			{ID: "left-once", Rank: 1, Summary: "the chosen change"},
			{ID: "right", Rank: 2, Summary: "the other change"},
		},
		Rationale: "one subject per candidate port",
	}
	record, err := contracts.NewRecord(mustTypeRef(t, "selection/v1"), unique, uniqueBody)
	if err != nil {
		t.Fatal(err)
	}
	validationContext := candidateValidationContextFor(
		t, map[string]snapshot.SnapshotRef{"left": left, "right": right}, "left", "right",
	)
	if _, err := validateFiles(t, "selection/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, validationContext); err != nil {
		t.Fatalf("validation of a unique-port selection failed: %v", err)
	}
}

func TestSelectionRecordRequiresAtLeastOneDeclaredCandidatePort(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 51, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("7", 64))}
	validationContext := validationContextFor(t, map[string]snapshot.SnapshotRef{"left": left})

	record, err := contracts.NewRecord(
		mustTypeRef(t, "selection/v1"),
		[]contracts.Subject{contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left)},
		contracts.SelectionBody{
			Selected:   "left",
			Candidates: []contracts.CandidateAssessment{{ID: "left", Rank: 1, Summary: "only"}},
			Rationale:  "no candidate port was declared for this step",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = validateFiles(t, "selection/v1", map[string][]byte{
		"record.json": marshalRecord(t, record),
	}, validationContext)
	if err == nil || !strings.Contains(err.Error(), "at least one declared candidate input port") {
		t.Fatalf("validation error = %v, want a declared-candidate-port failure", err)
	}
}

func assertContainsOneOf(t *testing.T, err error, wants []string) {
	t.Helper()
	for _, want := range wants {
		if strings.Contains(err.Error(), want) {
			return
		}
	}
	t.Fatalf("error = %v, want it to contain one of %q", err, wants)
}

// TestStoredSelectionRevalidatesWithNoCandidatePortsAvailable is the read-time
// half of the gate split. Candidate ports are a server-side declaration that
// exists only in the sealing step's process memory and is persisted nowhere, so a
// reader loading a stored selection has none. If read-time revalidation demanded
// them, every sealed selection/v1 record would be permanently unreadable — a
// validator that cannot re-validate a stored record is a defect.
func TestStoredSelectionRevalidatesWithNoCandidatePortsAvailable(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 61, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("8", 64))}
	right := snapshot.SnapshotRef{ID: 62, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("9", 64))}

	// Sealed under a step that DID declare both candidate ports.
	sealing := candidateValidationContextFor(
		t, map[string]snapshot.SnapshotRef{"left": left, "right": right}, "left", "right",
	)
	record, err := contracts.NewRecord(
		mustTypeRef(t, "selection/v1"),
		[]contracts.Subject{
			contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
			contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
		},
		contracts.SelectionBody{
			Selected: "right",
			Candidates: []contracts.CandidateAssessment{
				{ID: "left", Rank: 2, Summary: "viable"},
				{ID: "right", Rank: 1, Summary: "best"},
			},
			Rationale: "Right has the strongest evidence.",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"record.json": marshalRecord(t, record)}
	if _, err := validateFiles(t, "selection/v1", files, sealing); err != nil {
		t.Fatalf("seal-time admission of a valid selection: %v", err)
	}

	// Read back with NOTHING: no inputs, no candidate ports, no opener.
	reading := emptyValidationContext(t)
	if _, err := revalidateSealedFiles(t, "selection/v1", files, reading); err != nil {
		t.Fatalf("read-time revalidation of a stored selection with no candidate ports: %v", err)
	}
	stored := readSealedSelection(t, files)
	if stored.Body.Selected != "right" {
		t.Fatalf("stored selected = %q, want %q", stored.Body.Selected, "right")
	}
	if len(stored.Subjects) != 2 {
		t.Fatalf("stored subjects = %+v, want the two sealed candidate subjects", stored.Subjects)
	}
}

// TestStoredSelectionRevalidationStillRejectsANonSubjectSelection proves the read
// gate is not a rubber stamp. Taking candidacy from the sealed subject roles does
// not mean trusting the body: a selection of something that was never a subject
// is still rejected with no declarations at all.
func TestStoredSelectionRevalidationStillRejectsANonSubjectSelection(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 71, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("a", 64))}
	right := snapshot.SnapshotRef{ID: 72, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("b", 64))}

	for name, body := range map[string]contracts.SelectionBody{
		"selected is assessed but was never a subject": {
			Selected: "ghost",
			Candidates: []contracts.CandidateAssessment{
				{ID: "ghost", Rank: 1, Summary: "never a subject"},
				{ID: "left", Rank: 2, Summary: "a subject"},
				{ID: "right", Rank: 3, Summary: "a subject"},
			},
			Rationale: "selecting something that was never a subject",
		},
		"selected is not assessed at all": {
			Selected: "ghost",
			Candidates: []contracts.CandidateAssessment{
				{ID: "left", Rank: 1, Summary: "a subject"},
				{ID: "right", Rank: 2, Summary: "a subject"},
			},
			Rationale: "selecting something outside the assessed set",
		},
	} {
		t.Run(name, func(t *testing.T) {
			// Hand-built sealed bytes: seal-time admission would have refused
			// these, which is exactly why the read gate must refuse them too.
			record := contracts.Record[contracts.SelectionBody]{
				RecordVersion: contracts.RecordVersion,
				Type:          mustTypeRef(t, "selection/v1"),
				Schema:        mustSelectionSchema(t),
				Subjects: []contracts.Subject{
					contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
					contracts.SubjectFromInput("right", contracts.SubjectRoleCandidate, "right", right),
				},
				Body: body,
			}
			files := map[string][]byte{"record.json": marshalRecord(t, record)}
			if _, err := revalidateSealedFiles(t, "selection/v1", files, emptyValidationContext(t)); err == nil {
				t.Fatal("read-time revalidation accepted a selection whose selected subject was never a subject")
			}
		})
	}
}

func readSealedSelection(t *testing.T, files map[string][]byte) contracts.Record[contracts.SelectionBody] {
	t.Helper()
	root, err := os.OpenRoot(writeTree(t, files))
	if err != nil {
		t.Fatalf("OpenRoot(): %v", err)
	}
	defer root.Close()
	record, err := contracts.ReadSealedSelectionRecord(context.Background(), root)
	if err != nil {
		t.Fatalf("ReadSealedSelectionRecord(): %v", err)
	}
	return record
}

func mustSelectionSchema(t *testing.T) snapshot.Digest {
	t.Helper()
	digest, found := contracts.SchemaDigestFor(mustTypeRef(t, "selection/v1"))
	if !found {
		t.Fatal("SchemaDigestFor(selection/v1) not found")
	}
	return digest
}

// TestSealTimeSelectionIgnoresProducerClaimedCandidateRoles is the guard that the
// read-time relaxation did not leak into the write gate. Read-time candidacy is
// taken from the sealed subject roles; seal-time candidacy must still come only
// from the server's port declarations, or a producer could mint candidacy for
// itself by writing candidate on every subject it likes.
func TestSealTimeSelectionIgnoresProducerClaimedCandidateRoles(t *testing.T) {
	changeType := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 81, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("c", 64))}
	rubric := snapshot.SnapshotRef{ID: 82, Type: changeType, Digest: mustDigest(t, "sha256:"+strings.Repeat("d", 64))}

	// The step declares exactly one candidate port. The producer writes both of
	// its inputs as candidate subjects, promoting the rubric it was given for
	// context into something selectable.
	sealing := candidateValidationContextFor(
		t, map[string]snapshot.SnapshotRef{"left": left, "rubric": rubric}, "left",
	)
	record := contracts.Record[contracts.SelectionBody]{
		RecordVersion: contracts.RecordVersion,
		Type:          mustTypeRef(t, "selection/v1"),
		Schema:        mustSelectionSchema(t),
		Subjects: []contracts.Subject{
			contracts.SubjectFromInput("left", contracts.SubjectRoleCandidate, "left", left),
			contracts.SubjectFromInput("rubric", contracts.SubjectRoleCandidate, "rubric", rubric),
		},
		Body: contracts.SelectionBody{
			Selected: "rubric",
			Candidates: []contracts.CandidateAssessment{
				{ID: "left", Rank: 2, Summary: "the only declared candidate"},
				{ID: "rubric", Rank: 1, Summary: "context promoted to a candidate"},
			},
			Rationale: "claiming candidacy for an input that was never a candidate port",
		},
	}
	files := map[string][]byte{"record.json": marshalRecord(t, record)}
	_, err := validateFiles(t, "selection/v1", files, sealing)
	if err == nil {
		t.Fatal("seal-time admission accepted producer-claimed candidacy for an undeclared port")
	}
	if !strings.Contains(err.Error(), "is not a declared candidate port") {
		t.Fatalf("seal-time error = %v, want it to name the undeclared candidate port", err)
	}
}
