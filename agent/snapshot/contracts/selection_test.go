package contracts_test

import (
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
)

func TestSelectionRecordResolvesAnExistingExactCandidate(t *testing.T) {
	typeRef := mustTypeRef(t, "repository-change/v1")
	left := snapshot.SnapshotRef{ID: 11, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("a", 64))}
	right := snapshot.SnapshotRef{ID: 12, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("b", 64))}
	validationContext := validationContextFor(t, map[string]snapshot.SnapshotRef{"left": left, "right": right})

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
	selected, err := contracts.ResolveSelection(record, validationContext)
	if err != nil {
		t.Fatalf("ResolveSelection(): %v", err)
	}
	if selected != right {
		t.Fatalf("selected = %+v, want %+v", selected, right)
	}
}

func TestSelectionRecordRequiresEveryExactCandidateAndStableRanks(t *testing.T) {
	typeRef := mustTypeRef(t, "review/v1")
	left := snapshot.SnapshotRef{ID: 21, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("c", 64))}
	right := snapshot.SnapshotRef{ID: 22, Type: typeRef, Digest: mustDigest(t, "sha256:"+strings.Repeat("d", 64))}
	validationContext := validationContextFor(t, map[string]snapshot.SnapshotRef{"left": left, "right": right})
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
	}, validationContext); err == nil || (!strings.Contains(err.Error(), "every exposed input") && !strings.Contains(err.Error(), "rank")) {
		t.Fatalf("validation error = %v, want exact candidate or rank failure", err)
	}
}
