package hangar

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	afterCapture         func(*CapturedTree) error
	beforeLock           func() error
	afterStage           func(string) error
	beforePublish        func() error
	afterDestinationOpen func() error
	beforePayloadSeal    func() error
	beforeReceipt        func() error
	beforeReceiptRename  func() error
	afterReceipt         func() error
	duringRetryCompare   func() error
	beforeRootChmod      func() error
	beforeRootSync       func() error
}

// OpenRoot returns a new descriptor-anchored view of the verified extracted
// tree. It never resolves CapturedTree.Root after Capture returns.
func (tree *CapturedTree) OpenRoot() (*os.Root, error) {
	if tree == nil {
		return nil, fmt.Errorf("hangar: captured tree is required")
	}
	tree.closeMu.Lock()
	defer tree.closeMu.Unlock()
	if tree.closed || tree.materializationRoot == nil {
		return nil, fmt.Errorf("hangar: captured tree is closed")
	}
	root, err := tree.materializationRoot.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("hangar: duplicate captured tree root: %w", err)
	}
	return root, nil
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
	if materializer.hooks.afterCapture != nil {
		if err := materializer.hooks.afterCapture(captured); err != nil {
			return fmt.Errorf("hangar: after capturing materialization tree: %w", err)
		}
	}
	source, err := captured.OpenRoot()
	if err != nil {
		return err
	}
	defer source.Close()
	return materializeCapturedTree(ctx, materializer.StoragePath, handle, volume, ref, source, materializer.hooks)
}
