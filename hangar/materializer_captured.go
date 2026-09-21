package hangar

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Materialize installs an already verified private copy beneath an anchored
// steps directory. Managed readers acquire and release their object lease while
// producing this copy; installation needs no further object-store authority.
// Strict-input reads and managed reads share the same sealed, atomic publication.
func (tree *CapturedTree) Materialize(ctx context.Context, steps *os.Root, ref TreeRef, handle, volume string) (err error) {
	if err := ref.Validate(); err != nil {
		return err
	}
	if steps == nil || !validMaterializationSegment(handle) || !validMaterializationSegment(volume) {
		return fmt.Errorf("hangar: an anchored steps directory and canonical destination segments are required")
	}
	if tree == nil || tree.Digest != ref.Digest {
		return fmt.Errorf("hangar: captured tree differs from the tree reference: %w", ErrCorrupt)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := tree.OpenRoot()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, source.Close()) }()
	return materializeCapturedTreeRoot(ctx, steps, handle, volume, ref, source)
}
