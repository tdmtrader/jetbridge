package output

import (
	"fmt"
	"github.com/concourse/concourse/hangar"
)

// ManagedReadRequest carries authority for one exact tree and one consumer
// destination. The warrant stays between the control plane and the output daemon.
type ManagedReadRequest struct {
	Ref         hangar.TreeRef  `json:"ref"`
	Destination ReadDestination `json:"destination"`
	Warrant     string          `json:"warrant"`
}

// ReadAttributesHeader contains the canonical JSON TreeAttributes projection.
const ReadAttributesHeader = "Hangar-Tree-Attributes"

func (request ManagedReadRequest) Validate() error {
	if err := request.Ref.Validate(); err != nil {
		return fmt.Errorf("%w: invalid managed-read ref", ErrIncomplete)
	}
	if err := request.Destination.Validate(); err != nil {
		return err
	}
	if request.Warrant == "" || len(request.Warrant) > MaxReadWarrantBytes {
		return fmt.Errorf("%w: invalid managed-read warrant size", ErrIncomplete)
	}
	return nil
}
