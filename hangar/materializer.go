package hangar

import (
	"context"
	"errors"
	"fmt"
)

const materializationReceiptName = ".hangar-materialized"

type Materializer struct {
	Store         Store
	Canonicalizer Canonicalizer
	StoragePath   string
	MaxTreeBytes  int64
	hooks         materializerHooks
}

type materializerHooks struct {
	afterStage           func(string) error
	beforePublish        func() error
	afterDestinationOpen func() error
	beforeReceipt        func() error
	afterReceipt         func() error
}

func (materializer *Materializer) Materialize(ctx context.Context, ref TreeRef, handle, volume string) error {
	if materializer == nil || materializer.Store == nil {
		return fmt.Errorf("hangar: materializer store is required")
	}
	if err := ref.Validate(); err != nil {
		return err
	}
	if !validMaterializationSegment(handle) || !validMaterializationSegment(volume) {
		return fmt.Errorf("hangar: materialization handle and volume must be canonical path segments")
	}
	if materializer.StoragePath == "" {
		return fmt.Errorf("hangar: materialization storage path is required")
	}
	if materializer.MaxTreeBytes <= 0 {
		return fmt.Errorf("hangar: maximum materialized tree bytes must be positive")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	archive, attributes, err := materializer.Store.OpenTree(ctx, ref, materializer.MaxTreeBytes)
	if err != nil {
		return fmt.Errorf("hangar: open exact tree for materialization: %w", err)
	}
	if archive == nil {
		return fmt.Errorf("hangar: open exact tree for materialization: %w", ErrCorrupt)
	}
	if attributes.Ref != ref {
		_ = archive.Close()
		return fmt.Errorf("hangar: opened tree identity differs from request: %w", ErrCorrupt)
	}
	captured, captureErr := materializer.Canonicalizer.Capture(ctx, archive)
	closeErr := archive.Close()
	if captureErr != nil || closeErr != nil {
		if captured != nil {
			_ = captured.Close()
		}
		return errors.Join(captureErr, closeErr)
	}
	defer captured.Close()
	if captured.Digest != ref.Digest {
		return fmt.Errorf("hangar: captured tree digest differs from exact reference: %w", ErrCorrupt)
	}
	return materializeCapturedTree(ctx, materializer.StoragePath, handle, volume, ref, captured.Root, materializer.hooks)
}
