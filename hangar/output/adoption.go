package output

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// AdoptionOutcome is what an adoption attempt concluded.
//
// Every member but the first is a REFUSAL, and they are distinct members rather
// than one error because the operator question they answer is different in each
// case: an object protected by an unresolved reservation is a capture still
// running, an object inside its publication grace is a capture that may still
// retry, and an object whose capture has not settled is a source still held on
// a node. Collapsing them would turn "why is this object still here" into a
// question with one answer that is never the useful one.
//
// None of them is a miss. Req 27 governs every typed outcome in this plane, and
// adoption is where the temptation is strongest: an object with no lifecycle
// row looks exactly like a cache miss if the only question asked is "is it
// registered".
type AdoptionOutcome string

const (
	// AdoptionAdopted is the only member that writes a lifecycle row. It
	// records lifecycle state and nothing else: adoption never invents a
	// capture, a receipt or a binding.
	AdoptionAdopted AdoptionOutcome = "adopted"

	// AdoptionAlreadyRegistered is a generation that already has a lifecycle
	// row. Nothing is owed and nothing is written.
	AdoptionAlreadyRegistered AdoptionOutcome = "already_registered"

	// AdoptionProtectedByReservation is Req 40's shield. An unresolved
	// reservation protects its server-derived (scope, digest) correlation from
	// adoption even before a generation is known, and it does so REGARDLESS of
	// how much grace has elapsed: grace reduces work and provides recovery
	// margin, and it is never the claim/reclaim mutex.
	AdoptionProtectedByReservation AdoptionOutcome = "protected_by_reservation"

	// AdoptionWithinPublicationGrace is a marked object whose grace has not
	// elapsed. Its capture may still be legitimately retrying.
	AdoptionWithinPublicationGrace AdoptionOutcome = "within_publication_grace"

	// AdoptionCaptureNotSettled is a correlated capture that has a terminal
	// state but has not settled -- typically a source still held on a node
	// whose release nobody has acknowledged. Req 40 requires the release, not
	// only the decision.
	AdoptionCaptureNotSettled AdoptionOutcome = "capture_not_settled"

	// AdoptionBeforeCaptureDeadline is a correlated capture whose deadline plus
	// safety margin has not passed on the database clock. It is a separate
	// member from the grace one because they are different clocks measuring
	// different things: grace runs from the OBJECT's creation, and this runs
	// from the CAPTURE's deadline.
	AdoptionBeforeCaptureDeadline AdoptionOutcome = "before_capture_deadline"

	// AdoptionForeignEpoch is a marked object from another activation epoch's
	// namespace. It is recorded for diagnosis and never relabelled, adopted or
	// deleted: adopting it would record a lifecycle row under an epoch whose
	// attestation does not cover it, and relabelling it is exactly what Req 45
	// forbids for every object this cohort did not create.
	AdoptionForeignEpoch AdoptionOutcome = "foreign_epoch"
)

func AdoptionOutcomes() []AdoptionOutcome {
	return []AdoptionOutcome{
		AdoptionAdopted,
		AdoptionAlreadyRegistered,
		AdoptionProtectedByReservation,
		AdoptionWithinPublicationGrace,
		AdoptionCaptureNotSettled,
		AdoptionBeforeCaptureDeadline,
		AdoptionForeignEpoch,
	}
}

func ParseAdoptionOutcome(value string) (AdoptionOutcome, error) {
	for _, member := range AdoptionOutcomes() {
		if string(member) == value {
			return member, nil
		}
	}

	return "", fmt.Errorf("%w: adoption outcome %q; the vocabulary is %v",
		ErrUnknownMember, value, AdoptionOutcomes())
}

func (outcome *AdoptionOutcome) UnmarshalJSON(raw []byte) error {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return err
	}
	parsed, err := ParseAdoptionOutcome(text)
	if err != nil {
		return err
	}
	*outcome = parsed

	return nil
}

func (outcome AdoptionOutcome) Validate() error {
	_, err := ParseAdoptionOutcome(string(outcome))

	return err
}

// Adopted reports whether the outcome wrote a lifecycle row.
func (outcome AdoptionOutcome) Adopted() bool { return outcome == AdoptionAdopted }

// AdoptionRequest is one marked generation offered for adoption, with the
// evidence the sweep actually observed.
//
// The MARKER is a parameter and not something the repository reads back,
// because the repository cannot read an object: verifying the marker is the
// inventory principal's job (list and get, Req 54(b)), and a repository that
// adopted a ref on the caller's word alone would adopt anything a caller named.
// So the request carries what was read off the object -- its marker, its
// metageneration, its server-reported creation time -- and the repository
// checks those against each other and against the correlations it CAN read.
//
// It carries no json tags: no adoption request crosses a wire. The inventory
// controller is one process holding one database handle, and a type that said
// it were frozen would be promising a shape to an implementation that does not
// exist.
type AdoptionRequest struct {
	ProtocolVersion string
	ActivationEpoch executioncontrol.ActivationEpoch
	Ref             hangar.TreeRef
	Metageneration  int64
	Marker          ObjectMarker
	CreatedAt       Timestamp

	// Grace is the configured publication grace, and SafetyMargin is how far a
	// correlated capture's deadline must be in the past. They are parameters
	// rather than constants because a deployment may configure the first, and
	// because the pair is the one place Req 39's cross-constraint is applied to
	// a real object.
	Grace        time.Duration
	SafetyMargin time.Duration
}

func (request AdoptionRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if request.ActivationEpoch == 0 {
		return fmt.Errorf("%w: adoption names no activation epoch", ErrIncomplete)
	}
	if err := request.Ref.Validate(); err != nil {
		return err
	}
	if request.Metageneration <= 0 {
		return fmt.Errorf("%w: no metageneration was observed for %s/%s/%d",
			ErrIncomplete, request.Ref.Scope, request.Ref.Digest, request.Ref.Generation)
	}
	if err := request.Marker.Validate(); err != nil {
		return err
	}
	// The marker has to describe the object it was read off. Without this the
	// "verified output marker" of Req 45 would be satisfied by any marker at
	// all, including one copied from a different tree.
	if !request.Marker.Matches(request.Ref) {
		return fmt.Errorf("%w: the object at generation %d is marked %s/%s and the ref names "+
			"%s/%s; a marker that does not describe its own object is not verification",
			ErrCorrupt, request.Ref.Generation, request.Marker.Scope, request.Marker.Digest,
			request.Ref.Scope, request.Ref.Digest)
	}
	if request.CreatedAt.IsZero() {
		return fmt.Errorf("%w: adoption observed no creation time, and publication grace is "+
			"measured from it", ErrIncomplete)
	}
	if err := ValidatePublicationGrace(request.Grace, MaxCaptureDeadline); err != nil {
		return err
	}
	if request.SafetyMargin < PublicationGraceMargin {
		return fmt.Errorf("%w: an adoption safety margin of %s is below the %s floor",
			ErrIncomplete, request.SafetyMargin, PublicationGraceMargin)
	}

	return nil
}
