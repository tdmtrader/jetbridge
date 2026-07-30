package directgit

import (
	"context"
	"errors"
	"fmt"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
)

// VerifiedChangeMaterializer extracts the nested git-tree payload from an
// already verified repository-change/v1 value into private bounded scratch and
// proves its declared commits, tree, ancestry, and object database before it is
// returned to a transport.
type VerifiedChangeMaterializer struct {
	backend *Backend
}

func NewVerifiedChangeMaterializer(
	runner Runner,
	tempDir string,
	canonicalizer snapshot.Canonicalizer,
) (*VerifiedChangeMaterializer, error) {
	backend, err := NewBackend(runner, tempDir)
	if err != nil {
		return nil, err
	}
	backend.canonicalize = canonicalizer
	return &VerifiedChangeMaterializer{backend: backend}, nil
}

// VerifiedChange owns both the nested canonical tree and its descriptor-bound
// scratch directory. Close must be called after the transport returns.
type VerifiedChange struct {
	repository string
	tree       *snapshot.CapturedTree
	scratch    *privateScratch
}

func (change *VerifiedChange) Repository() string {
	if change == nil {
		return ""
	}
	return change.repository
}

func (change *VerifiedChange) Close() error {
	if change == nil {
		return nil
	}
	var treeErr error
	if change.tree != nil {
		treeErr = change.tree.Close()
	}
	var scratchErr error
	if change.scratch != nil {
		scratchErr = change.scratch.Close()
	}
	return errors.Join(treeErr, scratchErr)
}

func (materializer *VerifiedChangeMaterializer) Materialize(
	ctx context.Context,
	change publisher.RepositoryChange,
) (verified *VerifiedChange, returnedErr error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if materializer == nil || materializer.backend == nil {
		return nil, fmt.Errorf("direct git: verified change materializer is required")
	}
	if err := change.Validate(); err != nil {
		return nil, err
	}
	if !validObjectID(change.BaseSHA) || !validObjectID(change.ResultSHA) ||
		len(change.BaseSHA) != len(change.ResultSHA) ||
		!validMaterializedRoot(change.MaterializedRoot) {
		return nil, fmt.Errorf("direct git: repository change materialization is invalid")
	}

	scratch, err := materializer.backend.tempParent.createPrivateScratch(
		"concourse-direct-git-change-",
	)
	if err != nil {
		return nil, err
	}
	var tree *snapshot.CapturedTree
	defer func() {
		if verified != nil {
			return
		}
		var treeErr error
		if tree != nil {
			treeErr = tree.Close()
		}
		returnedErr = errors.Join(returnedErr, treeErr, scratch.Close())
	}()
	if err := scratch.Verify(); err != nil {
		return nil, err
	}
	operation := publisher.GitOperation{
		BaseSHA:          change.BaseSHA,
		ResultSHA:        change.ResultSHA,
		MaterializedRoot: change.MaterializedRoot,
	}
	document, tree, err := materializer.backend.materializeVerifiedChange(
		ctx,
		operation,
		scratch,
	)
	if err != nil {
		return nil, err
	}
	if err := materializer.backend.verifyRepository(
		ctx,
		scratch,
		tree,
		document,
	); err != nil {
		return nil, err
	}
	verified = &VerifiedChange{
		repository: tree.Root,
		tree:       tree,
		scratch:    scratch,
	}
	return verified, nil
}
