package output

// The publish route's request and result.
//
// What is NOT here is the point. There is no bucket, no scope, no object key
// and no tar body: the daemon publishes the SEALED TREE it is already holding,
// under a namespace it derived from authenticated configuration and the active
// epoch. A caller cannot choose where the bytes go because it does not hand
// over any bytes.
//
// Namespace is present anyway, and present for one reason: a hostile client
// really can put a bucket field in a request body, and the difference between
// ignoring it and refusing it with a message is whether an operator ever finds
// out. Validate refuses any of its fields being set.

import (
	"fmt"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// PublicationRequest asks the daemon to publish a sealed source incarnation.
type PublicationRequest struct {
	ProtocolVersion string                           `json:"protocol_version"`
	Execution       executioncontrol.Identity        `json:"execution"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	HandoffID       HandoffID                        `json:"handoff_id"`
	ReservationID   ReservationID                    `json:"reservation_id"`
	CaptureFence    CaptureFence                     `json:"capture_fence"`
	Namespace       CallerNamespaceRequest           `json:"namespace"`
}

func (request PublicationRequest) Validate() error {
	if err := validateProtocol(request.ProtocolVersion); err != nil {
		return err
	}
	if err := request.Execution.Validate(); err != nil {
		return err
	}
	if request.ActivationEpoch == 0 {
		return fmt.Errorf("%w: a publication names no activation epoch", ErrIncomplete)
	}
	if err := request.HandoffID.Validate(); err != nil {
		return err
	}
	if err := request.ReservationID.Validate(); err != nil {
		return err
	}
	if request.CaptureFence == 0 {
		return fmt.Errorf("%w: a publication names no capture fence", ErrIncomplete)
	}

	return request.Namespace.Validate()
}

// PublicationResult is the exact reference the store assigned, plus what the
// object turned out to be.
//
// Deduplicated is a fact about the store, not about this call: the same
// canonical bytes published twice are one object and two publications, and a
// caller has to be able to tell that apart from a collision.
type PublicationResult struct {
	ProtocolVersion string         `json:"protocol_version"`
	Ref             hangar.TreeRef `json:"ref"`
	Attributes      TreeAttributes `json:"attributes"`
	MarkerVersion   string         `json:"marker_version"`
	ReservationID   ReservationID  `json:"reservation_id"`
	Deduplicated    bool           `json:"deduplicated"`
	Metageneration  int64          `json:"metageneration"`
}

func (result PublicationResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.Ref.Validate(); err != nil {
		return err
	}
	if result.MarkerVersion != MarkerVersion {
		return fmt.Errorf("%w: the published object is marked %q and this cohort writes %q",
			ErrUnknownMember, result.MarkerVersion, MarkerVersion)
	}
	if err := result.ReservationID.Validate(); err != nil {
		return err
	}
	if result.Metageneration <= 0 {
		return fmt.Errorf("%w: the published object has no metageneration", ErrIncomplete)
	}

	return nil
}

// CanonicalizationResult is what a sealed source turns out to BE, before
// anything is created for it.
//
// It exists because requirement 21 puts a durable step between two things the
// publish route does in one call. "After canonicalization and before the first
// GCS create, the current capture owner durably resolves its reservation to
// the server-derived scope and logical digest" is an ordering, and an ordering
// needs the first half to be observable on its own -- otherwise the resolution
// can only be committed after the object exists, which is the sequence the
// requirement exists to forbid.
//
// It is deliberately NOT a CaptureAcknowledgement kind and carries no
// signature. A canonicalization authorizes nothing and proves nothing about
// the execution beyond a seal this daemon already recorded; the facts it
// carries are re-derived by the publish that follows, and the control plane
// compares the two. Putting it in the ledger's signed vocabulary would add a
// statement no receipt ever verifies.
//
// Scope and Digest are server-derived, exactly as they are in the publish: the
// scope from the namespace this daemon resolved from authenticated
// configuration and the active epoch, the digest from the foundation's
// canonicalizer over the sealed tree. There is no request field for either.
type CanonicalizationResult struct {
	ProtocolVersion string        `json:"protocol_version"`
	HandoffID       HandoffID     `json:"handoff_id"`
	ReservationID   ReservationID `json:"reservation_id"`
	CaptureFence    CaptureFence  `json:"capture_fence"`
	Scope           hangar.Scope  `json:"scope"`
	Digest          hangar.Digest `json:"digest"`
	LogicalBytes    int64         `json:"logical_bytes"`
	ObservedAt      Timestamp     `json:"observed_at"`
}

func (result CanonicalizationResult) Validate() error {
	if err := validateProtocol(result.ProtocolVersion); err != nil {
		return err
	}
	if err := result.HandoffID.Validate(); err != nil {
		return err
	}
	if err := result.ReservationID.Validate(); err != nil {
		return err
	}
	if result.CaptureFence == 0 {
		return fmt.Errorf("%w: a canonicalization names no capture fence; a stale owner may not "+
			"resolve a logical reservation", ErrIncomplete)
	}
	if err := result.Scope.Validate(); err != nil {
		return err
	}
	if err := result.Digest.Validate(); err != nil {
		return err
	}
	if result.LogicalBytes <= 0 {
		return fmt.Errorf("%w: the canonical tree measured %d logical bytes",
			ErrIncomplete, result.LogicalBytes)
	}

	return result.ObservedAt.Validate()
}
