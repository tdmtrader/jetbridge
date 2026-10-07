package db

import (
	"context"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// HangarOrphanVerdict is what the database says about one listed object the
// orphan sweep found marked for this store.
type HangarOrphanVerdict string

const (
	// HangarOrphan has no lifecycle row and nothing about to give it one: the
	// sweep may delete its exact generation.
	HangarOrphan HangarOrphanVerdict = "orphan"

	// HangarOrphanRegistered has a lifecycle row, in any state. Reclamation
	// owns it; the sweep never does.
	HangarOrphanRegistered HangarOrphanVerdict = "registered"

	// HangarOrphanProtected has a pending or publishing capture, or an
	// unregistered input publication, of its tree: something is about to
	// register it, however old the object is.
	HangarOrphanProtected HangarOrphanVerdict = "protected"
)

// JudgeOrphan locks a listed object's logical correlation and exact lifecycle
// in the one Hangar lock order and says whether it is an orphan.
//
// The caller holds the transaction across its delete: a capture moving to
// publishing, or a registration of this exact generation, waits for the
// sweep's verdict and its act together, rather than landing between them.
func (repository *HangarOutputRepository) JudgeOrphan(ctx context.Context, tx output.Tx, ref hangar.TreeRef) (HangarOrphanVerdict, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	})
	if err != nil {
		return "", err
	}
	if _, registered := locks.Lifecycles[ref]; registered {
		return HangarOrphanRegistered, nil
	}

	var lifecycles, protecting int
	if err := hangarQueryRow(ctx, tx, `
		SELECT
			(SELECT count(*) FROM hangar_exact_lifecycles
			  WHERE scope = $1 AND digest = $2 AND generation = $3),
			(SELECT count(*) FROM hangar_captures
			  WHERE scope = $1 AND digest = $2 AND state IN ('pending', 'publishing')) +
			(SELECT count(*) FROM hangar_input_publications
			  WHERE scope = $1 AND digest = $2 AND lifecycle_id IS NULL
			    AND expires_at > clock_timestamp())`,
		[]any{string(ref.Scope), string(ref.Digest), ref.Generation},
		&lifecycles, &protecting); err != nil {
		return "", err
	}
	switch {
	case lifecycles > 0:
		return HangarOrphanRegistered, nil
	case protecting > 0:
		return HangarOrphanProtected, nil
	default:
		return HangarOrphan, nil
	}
}
