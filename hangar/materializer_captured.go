package hangar

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// Materialize installs an already verified private copy beneath an anchored
// steps directory, sealed read-only and published atomically with its
// receipt. The reader holds its claim while producing this copy; installation
// needs no further object-store authority.
func (tree *CapturedTree) Materialize(ctx context.Context, steps *os.Root, ref TreeRef, handle, volume string) error {
	return tree.materialize(ctx, steps, ref, handle, volume, materializerHooks{})
}

func (tree *CapturedTree) materialize(ctx context.Context, steps *os.Root, ref TreeRef, handle, volume string, hooks materializerHooks) (err error) {
	if err := ref.Validate(); err != nil {
		return err
	}
	if steps == nil || !validWarrantSegment(handle) || !validWarrantSegment(volume) {
		return errors.New("hangar: an anchored steps directory and canonical destination segments are required")
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
	return materializeCapturedTreeRoot(ctx, steps, handle, volume, ref, source, hooks)
}
