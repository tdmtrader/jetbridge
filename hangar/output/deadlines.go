package output

import (
	"fmt"
	"time"
)

// The frozen defaults and their configurable ranges.
//
// They are constants rather than configuration defaults scattered across flag
// declarations because two of them constrain each other, and the constraint is
// the interesting part: publication grace must exceed the maximum capture
// deadline by an hour, or the orphan sweep could delete an object whose own
// capture was still legitimately retrying. Startup validates them together.
const (
	// DefaultOperationTimeout is shared by the output plane and its callers.
	DefaultOperationTimeout = time.Minute

	// DefaultCaptureDeadline is how long a capture may remain unresolved before
	// it must be terminally settled. It is measured on the database clock.
	DefaultCaptureDeadline = 24 * time.Hour
	MinCaptureDeadline     = time.Hour
	MaxCaptureDeadline     = 7 * 24 * time.Hour

	// DefaultPublicationGrace is how long a marked, unregistered object is left
	// alone before the orphan sweep may treat it as an orphan, and how long a
	// registered generation is left alone before the reclaim pass may delete
	// it. Grace reduces work and provides recovery margin; it is never the
	// claim/reclaim mutex.
	DefaultPublicationGrace = 8 * 24 * time.Hour
	MaxPublicationGrace     = 30 * 24 * time.Hour

	// PublicationGraceMargin is how far publication grace must exceed the
	// configured maximum capture deadline. Without it, an object could become
	// an orphan while its own capture was still legitimately retrying.
	PublicationGraceMargin = time.Hour

	// ReadClaimMargin is added to a read's materialization timeout to make the
	// term of the reader's claim: the claim outlives the read it protects by
	// this much, and then lapses on the database clock whether or not the
	// web got to give it back.
	ReadClaimMargin = 5 * time.Minute
)

// ValidateCaptureDeadline refuses a configured capture deadline outside
// MinCaptureDeadline..MaxCaptureDeadline.
func ValidateCaptureDeadline(deadline time.Duration) error {
	if deadline < MinCaptureDeadline || deadline > MaxCaptureDeadline {
		return fmt.Errorf("%w: capture deadline %s is outside %s..%s",
			ErrIncomplete, deadline, MinCaptureDeadline, MaxCaptureDeadline)
	}

	return nil
}

// ValidatePublicationGrace refuses a publication grace that does not exceed the
// maximum capture deadline by PublicationGraceMargin, or exceeds the maximum.
//
// The floor is derived from MaxCaptureDeadline, the plane-wide CEILING on any
// capture deadline, and not from a deployment-level maximum -- because there is
// no such thing to pass. A capture deadline is per-capture and is bounded above
// by this constant, so a grace above the constant plus an hour is conservative
// for every capture any deployment can predeclare.
func ValidatePublicationGrace(grace time.Duration) error {
	if grace > MaxPublicationGrace {
		return fmt.Errorf("%w: publication grace %s exceeds the maximum %s",
			ErrIncomplete, grace, MaxPublicationGrace)
	}
	if grace < MaxCaptureDeadline+PublicationGraceMargin {
		return fmt.Errorf("%w: publication grace %s does not exceed the maximum capture deadline "+
			"%s by at least %s; an object could become an orphan while its own capture was still "+
			"legitimately retrying", ErrIncomplete, grace, MaxCaptureDeadline,
			PublicationGraceMargin)
	}

	return nil
}

// ReadTransferTimeout leaves a bounded transport and verification margin beyond
// the node operation. Callers, initializers and startup budgeting share it.
func ReadTransferTimeout(operationTimeout time.Duration) time.Duration {
	return operationTimeout + time.Minute
}
