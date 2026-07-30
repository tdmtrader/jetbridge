package pullrequest

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

var ErrApprovedBaselineAuthority = errors.New(
	"pullrequest: exact approved-baseline authority is unavailable",
)

// ApprovedBaselineAuthoritySpec is the immutable publication fact that
// authorized the binding's current human-approved repository baseline. The
// initial value is authorized by accepted review; later values may only be
// authorized by an exact durable human wait.
type ApprovedBaselineAuthoritySpec struct {
	TeamID                  int
	BindingID               int64
	PublicationOccurrenceID int64
	Kind                    publisher.EvidenceKind
	Repository              snapshot.SnapshotRef
	Validation              snapshot.SnapshotRef
}

// ApprovedBaselineAuthority protects the exact repository and validation
// references resolved from one same-team authorizing publication occurrence.
// It deliberately carries no caller-authored policy projection.
type ApprovedBaselineAuthority struct {
	TeamID                  int
	BindingID               int64
	PublicationOccurrenceID int64
	Kind                    publisher.EvidenceKind
	Repository              snapshot.SnapshotRef
	Validation              snapshot.SnapshotRef

	authority *approvedBaselineAuthority
}

type approvedBaselineAuthority struct {
	value ApprovedBaselineAuthority
}

func NewApprovedBaselineAuthority(
	spec ApprovedBaselineAuthoritySpec,
) (ApprovedBaselineAuthority, error) {
	value := ApprovedBaselineAuthority{
		TeamID:                  spec.TeamID,
		BindingID:               spec.BindingID,
		PublicationOccurrenceID: spec.PublicationOccurrenceID,
		Kind:                    spec.Kind,
		Repository:              spec.Repository,
		Validation:              spec.Validation,
	}
	if err := validateApprovedBaselineAuthority(value); err != nil {
		return ApprovedBaselineAuthority{}, err
	}
	protected := value
	value.authority = &approvedBaselineAuthority{value: protected}
	return value, nil
}

func (value ApprovedBaselineAuthority) Protected() (
	ApprovedBaselineAuthority,
	error,
) {
	if value.authority == nil {
		return ApprovedBaselineAuthority{}, ErrApprovedBaselineAuthority
	}
	public := value
	public.authority = nil
	if !reflect.DeepEqual(public, value.authority.value) {
		return ApprovedBaselineAuthority{}, fmt.Errorf(
			"%w: authority changed after resolution",
			ErrApprovedBaselineAuthority,
		)
	}
	protected := value.authority.value
	protected.authority = value.authority
	return protected, nil
}

func validateApprovedBaselineAuthority(
	value ApprovedBaselineAuthority,
) error {
	if value.TeamID <= 0 ||
		value.BindingID <= 0 ||
		value.PublicationOccurrenceID <= 0 ||
		(value.Kind != publisher.EvidenceAcceptedReview &&
			value.Kind != publisher.EvidenceHumanWait) ||
		value.Repository.Validate() != nil ||
		value.Repository.Type != snapshot.TypeRef("repository/v1") ||
		value.Validation.Validate() != nil ||
		value.Validation.Type != snapshot.TypeRef("validation/v1") {
		return ErrApprovedBaselineAuthority
	}
	return nil
}

// ApprovedBaselineAuthorityLookup binds resolution to the exact PR binding
// and all three mutable baseline facts. A resolver may not look up an
// occurrence and then let the caller substitute another binding or different
// snapshot IDs after authorization.
type ApprovedBaselineAuthorityLookup struct {
	TeamID                  int
	BindingID               int64
	PublicationOccurrenceID int64
	RepositorySnapshotID    snapshot.SnapshotID
	ValidationSnapshotID    snapshot.SnapshotID
}

func (lookup ApprovedBaselineAuthorityLookup) Validate() error {
	if lookup.TeamID <= 0 ||
		lookup.BindingID <= 0 ||
		lookup.PublicationOccurrenceID <= 0 ||
		lookup.RepositorySnapshotID <= 0 ||
		lookup.ValidationSnapshotID <= 0 {
		return ErrApprovedBaselineAuthority
	}
	return nil
}

type ApprovedBaselineAuthorityResolver interface {
	ResolveApprovedBaselineAuthority(
		context.Context,
		ApprovedBaselineAuthorityLookup,
	) (ApprovedBaselineAuthority, bool, error)
}
