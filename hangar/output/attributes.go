package output

import (
	"fmt"

	"github.com/concourse/concourse/hangar"
)

// TreeAttributes is the wire projection of hangar.TreeAttributes.
//
// It declares no new fact. Every field is the foundation's, in the foundation's
// order, and AttributesFromFoundation / Foundation convert between them without
// loss. The single difference is CreatedAt: Timestamp fixes its width at nine
// digits and requires UTC, so one instant has exactly one wire spelling.
type TreeAttributes struct {
	Ref          hangar.TreeRef `json:"ref"`
	StoredBytes  int64          `json:"stored_bytes"`
	LogicalBytes int64          `json:"logical_bytes"`
	CreatedAt    Timestamp      `json:"created_at"`
}

// AttributesFromFoundation projects the foundation's value onto the wire.
func AttributesFromFoundation(attributes hangar.TreeAttributes) TreeAttributes {
	return TreeAttributes{
		Ref:          attributes.Ref,
		StoredBytes:  attributes.StoredBytes,
		LogicalBytes: attributes.LogicalBytes,
		CreatedAt:    NewTimestamp(attributes.CreatedAt),
	}
}

// Foundation projects back. It normalizes the location to UTC, which is the
// point of the projection, and never moves the instant.
func (attributes TreeAttributes) Foundation() hangar.TreeAttributes {
	return hangar.TreeAttributes{
		Ref:          attributes.Ref,
		StoredBytes:  attributes.StoredBytes,
		LogicalBytes: attributes.LogicalBytes,
		CreatedAt:    attributes.CreatedAt.Time,
	}
}

func (attributes TreeAttributes) Validate() error {
	if err := attributes.Ref.Validate(); err != nil {
		return err
	}
	if attributes.StoredBytes < 0 || attributes.LogicalBytes < 0 {
		return fmt.Errorf("%w: attributes report a negative size", ErrCorrupt)
	}

	return attributes.CreatedAt.Validate()
}
