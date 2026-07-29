package checkpoint

import (
	"fmt"

	"github.com/concourse/concourse/agent/hangar"
)

// Archive is the immutable Hangar identity attached to a checkpoint. It
// deliberately contains no filesystem capture API: capture and restoration
// are runtime concerns that must consume this already-committed identity.
type Archive struct {
	Ref hangar.ObjectRef `json:"ref"`
}

func (archive Archive) Validate() error {
	if archive.Ref.Kind != hangar.KindCheckpoint {
		return fmt.Errorf("checkpoint: archive kind must be checkpoints")
	}
	if archive.Ref.Generation <= 0 {
		return fmt.Errorf("checkpoint: archive generation must be positive")
	}
	key, err := hangar.Key(archive.Ref.Kind, archive.Ref.Digest)
	if err != nil {
		return fmt.Errorf("checkpoint: archive identity: %w", err)
	}
	if archive.Ref.Key != key {
		return fmt.Errorf("checkpoint: archive key is not canonical for its digest")
	}
	return nil
}
