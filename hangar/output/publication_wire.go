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
