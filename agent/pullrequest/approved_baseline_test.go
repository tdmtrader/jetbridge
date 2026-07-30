package pullrequest

import (
	"errors"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

func TestApprovedBaselineAuthorityProtectsBothAuthorizationKinds(t *testing.T) {
	repository := approvedBaselineSnapshotRef(41, "repository/v1", 'a')
	validation := approvedBaselineSnapshotRef(42, "validation/v1", 'b')

	for _, kind := range []publisher.EvidenceKind{
		publisher.EvidenceAcceptedReview,
		publisher.EvidenceHumanWait,
	} {
		t.Run(string(kind), func(t *testing.T) {
			authority, err := NewApprovedBaselineAuthority(
				ApprovedBaselineAuthoritySpec{
					TeamID: 7, BindingID: 19,
					PublicationOccurrenceID: 81,
					Kind:                    kind,
					Repository:              repository,
					Validation:              validation,
				},
			)
			if err != nil {
				t.Fatal(err)
			}

			protected, err := authority.Protected()
			if err != nil {
				t.Fatal(err)
			}
			if protected.TeamID != 7 ||
				protected.BindingID != 19 ||
				protected.PublicationOccurrenceID != 81 ||
				protected.Kind != kind ||
				protected.Repository != repository ||
				protected.Validation != validation {
				t.Fatalf("protected authority = %#v", protected)
			}

			authority.Repository.ID++
			if _, err := authority.Protected(); !errors.Is(
				err, ErrApprovedBaselineAuthority,
			) {
				t.Fatalf("mutated authority error = %v", err)
			}
		})
	}
}

func TestApprovedBaselineAuthorityRejectsInvalidIdentityAndKind(t *testing.T) {
	valid := ApprovedBaselineAuthoritySpec{
		TeamID: 7, BindingID: 19,
		PublicationOccurrenceID: 81,
		Kind:                    publisher.EvidenceHumanWait,
		Repository: approvedBaselineSnapshotRef(
			41, "repository/v1", 'a',
		),
		Validation: approvedBaselineSnapshotRef(
			42, "validation/v1", 'b',
		),
	}
	tests := map[string]func(*ApprovedBaselineAuthoritySpec){
		"team": func(spec *ApprovedBaselineAuthoritySpec) {
			spec.TeamID = 0
		},
		"binding": func(spec *ApprovedBaselineAuthoritySpec) {
			spec.BindingID = 0
		},
		"occurrence": func(spec *ApprovedBaselineAuthoritySpec) {
			spec.PublicationOccurrenceID = 0
		},
		"kind": func(spec *ApprovedBaselineAuthoritySpec) {
			spec.Kind = publisher.EvidenceKind("forged")
		},
		"repository type": func(spec *ApprovedBaselineAuthoritySpec) {
			spec.Repository.Type = "repository-change/v1"
		},
		"validation type": func(spec *ApprovedBaselineAuthoritySpec) {
			spec.Validation.Type = "review/v1"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			spec := valid
			mutate(&spec)
			if _, err := NewApprovedBaselineAuthority(spec); !errors.Is(
				err, ErrApprovedBaselineAuthority,
			) {
				t.Fatalf("NewApprovedBaselineAuthority() error = %v", err)
			}
		})
	}
}

func TestApprovedBaselineAuthorityLookupBindsExpectedSnapshotIDs(t *testing.T) {
	lookup := ApprovedBaselineAuthorityLookup{
		TeamID: 7, BindingID: 19,
		PublicationOccurrenceID: 81,
		RepositorySnapshotID:    41,
		ValidationSnapshotID:    42,
	}
	if err := lookup.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	for name, mutate := range map[string]func(*ApprovedBaselineAuthorityLookup){
		"team": func(value *ApprovedBaselineAuthorityLookup) {
			value.TeamID = 0
		},
		"binding": func(value *ApprovedBaselineAuthorityLookup) {
			value.BindingID = 0
		},
		"occurrence": func(value *ApprovedBaselineAuthorityLookup) {
			value.PublicationOccurrenceID = 0
		},
		"repository": func(value *ApprovedBaselineAuthorityLookup) {
			value.RepositorySnapshotID = 0
		},
		"validation": func(value *ApprovedBaselineAuthorityLookup) {
			value.ValidationSnapshotID = 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := lookup
			mutate(&candidate)
			if !errors.Is(
				candidate.Validate(), ErrApprovedBaselineAuthority,
			) {
				t.Fatalf("Validate() error = %v", candidate.Validate())
			}
		})
	}
}

func approvedBaselineSnapshotRef(
	id snapshot.SnapshotID,
	typ snapshot.TypeRef,
	fill byte,
) snapshot.SnapshotRef {
	return snapshot.SnapshotRef{
		ID:   id,
		Type: typ,
		Digest: snapshot.Digest(
			"sha256:" + strings.Repeat(string(fill), 64),
		),
	}
}
