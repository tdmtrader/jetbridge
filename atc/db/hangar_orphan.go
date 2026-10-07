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

// JudgeOrphans is one page's verdicts in ONE statement, without locks: the
// sweep's first cut. Every ref it calls HangarOrphan is judged again, under
// the tree and row locks, by JudgeOrphan in the transaction that deletes it.
func (repository *HangarOutputRepository) JudgeOrphans(ctx context.Context, tx output.Tx, refs []hangar.TreeRef) (map[hangar.TreeRef]HangarOrphanVerdict, error) {
	verdicts := map[hangar.TreeRef]HangarOrphanVerdict{}
	if len(refs) == 0 {
		return verdicts, nil
	}
	scopes := make([]string, len(refs))
	digests := make([]string, len(refs))
	generations := make([]int64, len(refs))
	for i, ref := range refs {
		scopes[i], digests[i], generations[i] = string(ref.Scope), string(ref.Digest), ref.Generation
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT r.scope, r.digest, r.generation,
			EXISTS (SELECT 1 FROM hangar_exact_lifecycles l
			         WHERE l.scope = r.scope AND l.digest = r.digest AND l.generation = r.generation),
			EXISTS (SELECT 1 FROM hangar_captures c
			         WHERE c.scope = r.scope AND c.digest = r.digest AND c.state IN ('pending', 'publishing'))
			OR EXISTS (SELECT 1 FROM hangar_input_publications i
			         WHERE i.scope = r.scope AND i.digest = r.digest AND i.lifecycle_id IS NULL
			           AND i.expires_at > clock_timestamp())
		FROM unnest($1::text[], $2::text[], $3::bigint[]) AS r(scope, digest, generation)`,
		scopes, digests, generations)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	for rows.Next() {
		var scope, digest string
		var generation int64
		var registered, protected bool
		if err := rows.Scan(&scope, &digest, &generation, &registered, &protected); err != nil {
			return nil, err
		}
		ref := hangar.TreeRef{Scope: hangar.Scope(scope), Digest: hangar.Digest(digest), Generation: generation}
		switch {
		case registered:
			verdicts[ref] = HangarOrphanRegistered
		case protected:
			verdicts[ref] = HangarOrphanProtected
		default:
			verdicts[ref] = HangarOrphan
		}
	}

	return verdicts, rows.Err()
}
