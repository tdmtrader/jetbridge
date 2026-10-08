package hangar

import (
	"errors"
	"fmt"
	"os"
)

// materializationReceiptName is the read-only file a materialization leaves
// at the destination's root, holding the exact tree ref it installed. The
// managed-input init checks it before the task starts.
const materializationReceiptName = ".hangar-materialized"

// materializerHooks are test seams into the materialization sequence; the
// product path passes none.

type materializerHooks struct {
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
		return nil, errors.New("hangar: captured tree is required")
	}
	tree.closeMu.Lock()
	defer tree.closeMu.Unlock()
	if tree.closed || tree.materializationRoot == nil {
		return nil, errors.New("hangar: captured tree is closed")
	}
	root, err := tree.materializationRoot.OpenRoot(".")
	if err != nil {
		return nil, fmt.Errorf("hangar: duplicate captured tree root: %w", err)
	}
	return root, nil
}
