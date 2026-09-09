package output

import (
	"fmt"
	"strconv"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
)

// The custom-metadata keys carrying ownership evidence on a published object.
//
// They are written once, at creation, and the publisher principal has no
// permission to update them afterwards. That is what makes the marker evidence
// rather than a label: an object that has it was created by this system, and an
// object that lacks it was not, and no runtime identity can convert one into
// the other.
//
// Every key is generic Hangar metadata. There is no Run, workflow, ticket or
// consumer identifier here, and there is no object key or bucket either -- the
// reservation id is the correlation handle, and it is one Hangar generated.
const (
	MarkerKeyVersion       = "hangar-output-version"
	MarkerKeyScope         = "hangar-output-scope"
	MarkerKeyDigest        = "hangar-output-digest"
	MarkerKeyReservationID = "hangar-output-reservation-id"
	MarkerKeyActivation    = "hangar-output-activation-epoch"
	MarkerKeyCreatedAt     = "hangar-output-created-at"
)

// markerCreatedAtLayout is the nine-digit RFC 3339 form. Object metadata is
// string-to-string, so this is written by hand rather than by a JSON encoder,
// and it uses the same single spelling per instant as the rest of the wire.
const markerCreatedAtLayout = "2006-01-02T15:04:05.000000000Z07:00"

// ObjectMarker is the parsed form of that metadata.
//
// It deliberately carries the logical identity of the tree -- scope and digest
// -- and not its generation. The generation is assigned by the store at
// creation and is therefore not knowable at the moment the metadata is written;
// registration adds it, and inventory reads it from the object itself. Putting
// it here would have meant a marker that is a self-report of the very fact it
// is supposed to help verify.
type ObjectMarker struct {
	Version         string                           `json:"version"`
	Scope           hangar.Scope                     `json:"scope"`
	Digest          hangar.Digest                    `json:"digest"`
	ReservationID   ReservationID                    `json:"reservation_id"`
	ActivationEpoch executioncontrol.ActivationEpoch `json:"activation_epoch"`
	CreatedAt       Timestamp                        `json:"created_at"`
}

// Metadata renders the marker as object custom metadata.
func (marker ObjectMarker) Metadata() map[string]string {
	return map[string]string{
		MarkerKeyVersion:       marker.Version,
		MarkerKeyScope:         string(marker.Scope),
		MarkerKeyDigest:        string(marker.Digest),
		MarkerKeyReservationID: string(marker.ReservationID),
		MarkerKeyActivation:    strconv.FormatUint(uint64(marker.ActivationEpoch), 10),
		MarkerKeyCreatedAt:     marker.CreatedAt.UTC().Format(markerCreatedAtLayout),
	}
}

// ParseObjectMarker reads ownership evidence off an object.
//
// It fails closed in every direction that matters. Absent evidence is
// ErrNotFound, because an unmarked object is *unmanaged* -- it is never
// relabelled, adopted or deleted, and treating "no marker" as "not ours to
// worry about" is the only safe reading. A different marker version is
// ErrConflict rather than a parse failure, because a wrong version is a
// deliberate statement by some other cohort and must not be overwritten.
// Anything else malformed is ErrCorrupt, which becomes inventory debt.
func ParseObjectMarker(metadata map[string]string) (ObjectMarker, error) {
	version, ok := metadata[MarkerKeyVersion]
	if !ok || version == "" {
		return ObjectMarker{}, fmt.Errorf("%w: object carries no %s metadata, so it is unmanaged; "+
			"it is never relabelled, adopted or deleted", ErrNotFound, MarkerKeyVersion)
	}
	if version != MarkerVersion {
		return ObjectMarker{}, fmt.Errorf("%w: object carries marker version %q, this cohort "+
			"accepts %q", ErrConflict, version, MarkerVersion)
	}

	marker := ObjectMarker{
		Version:       version,
		Scope:         hangar.Scope(metadata[MarkerKeyScope]),
		Digest:        hangar.Digest(metadata[MarkerKeyDigest]),
		ReservationID: ReservationID(metadata[MarkerKeyReservationID]),
	}

	epoch, err := strconv.ParseUint(metadata[MarkerKeyActivation], 10, 64)
	if err != nil {
		return ObjectMarker{}, fmt.Errorf("%w: %s is not a number: %v",
			ErrCorrupt, MarkerKeyActivation, err)
	}
	marker.ActivationEpoch = executioncontrol.ActivationEpoch(epoch)

	createdAt, err := time.Parse(time.RFC3339Nano, metadata[MarkerKeyCreatedAt])
	if err != nil {
		return ObjectMarker{}, fmt.Errorf("%w: %s is not RFC 3339: %v",
			ErrCorrupt, MarkerKeyCreatedAt, err)
	}
	if createdAt.Location() != time.UTC {
		return ObjectMarker{}, fmt.Errorf("%w: %s is not UTC", ErrCorrupt, MarkerKeyCreatedAt)
	}
	marker.CreatedAt = NewTimestamp(createdAt)

	if err := marker.Validate(); err != nil {
		return ObjectMarker{}, err
	}

	return marker, nil
}

func (marker ObjectMarker) Validate() error {
	if marker.Version != MarkerVersion {
		return fmt.Errorf("%w: marker version %q, this cohort accepts %q",
			ErrConflict, marker.Version, MarkerVersion)
	}
	if err := marker.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: marker scope: %v", ErrCorrupt, err)
	}
	if err := marker.Digest.Validate(); err != nil {
		return fmt.Errorf("%w: marker digest: %v", ErrCorrupt, err)
	}
	if err := marker.ReservationID.Validate(); err != nil {
		return fmt.Errorf("%w: marker reservation id: %v", ErrCorrupt, err)
	}
	if marker.ActivationEpoch == 0 {
		return fmt.Errorf("%w: marker activation epoch is zero", ErrCorrupt)
	}
	if marker.CreatedAt.IsZero() {
		return fmt.Errorf("%w: marker creation time is zero", ErrCorrupt)
	}

	return nil
}

// Matches reports whether this marker describes the logical tree of ref. It is
// deliberately not an equality check on the generation: the marker never
// carries one.
func (marker ObjectMarker) Matches(ref hangar.TreeRef) bool {
	return marker.Scope == ref.Scope && marker.Digest == ref.Digest
}
