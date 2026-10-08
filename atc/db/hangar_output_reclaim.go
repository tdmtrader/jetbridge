package db

// The reclaim pass's durable half: the bounded candidate query, the hold that
// rechecks a candidate under the tree and lifecycle locks, and the stamp that
// records the delete.
//
// One transaction per generation. The pass holds the candidate's locks, asks
// the store to delete the exact generation, and stamps reclaimed_at in the
// same transaction only after the store answered. There is no admitted-but-
// not-finalized state to record: a pass that dies between the delete and the
// commit leaves the row registered, the next pass retries the delete and gets
// already-absent, and the exact generation is gone either way.

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// ReclaimableGenerations is the reclaim pass's bounded work query: registered
// generations past publication grace that no live claim, no pending or
// publishing capture and no unregistered input publication names, oldest
// first.
//
// It is what the pass selects and nothing it decides: every exclusion named
// here is rechecked by HoldForReclaim under the locks. Selecting on them here
// as well is the bound that stops a pass from opening a transaction per
// protected generation in the deployment.
//
// The grace comparison is made on the DATABASE clock against the row's own
// registered_at: registered_at is the instant this plane learned the
// generation exists, and the object create precedes the registration, so the
// wait is never shorter than grace measured from creation.
func (repository *HangarOutputRepository) ReclaimableGenerations(ctx context.Context, tx output.Tx, grace time.Duration, limit int) ([]hangar.TreeRef, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a reclaim pass is bounded; %d is not a batch",
			output.ErrIncomplete, limit)
	}
	if grace <= 0 {
		return nil, fmt.Errorf("%w: a reclaim pass names no publication grace, and elapsed grace "+
			"is one of its preconditions", output.ErrIncomplete)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT l.scope, l.digest, l.generation
		  FROM hangar_exact_lifecycles l
		 WHERE l.reclaimed_at IS NULL
		   AND l.registered_at <= now() - $1::interval
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_claims c
		        WHERE c.lifecycle_id = l.id AND c.released_at IS NULL
		          AND (c.expires_at IS NULL OR c.expires_at > now()))
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_captures p
		        WHERE p.scope = l.scope AND p.digest = l.digest
		          AND p.state IN ('pending', 'publishing'))
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_input_publications i
		        WHERE i.scope = l.scope AND i.digest = l.digest
		          AND i.lifecycle_id IS NULL AND i.expires_at > clock_timestamp())
		 ORDER BY l.registered_at, l.id
		 LIMIT $2`, hangarInterval(grace), limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var refs []hangar.TreeRef
	for rows.Next() {
		var scope, digest string
		var generation int64
		if err := rows.Scan(&scope, &digest, &generation); err != nil {
			return nil, err
		}
		refs = append(refs, hangar.TreeRef{
			Scope: hangar.Scope(scope), Digest: hangar.Digest(digest), Generation: generation,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return refs, nil
}

// HoldForReclaim takes the tree lock and the lifecycle row lock for one
// candidate and rechecks, under them, that it may be deleted: registered,
// past publication grace, no live claim, no pending or publishing capture or
// unregistered input publication of its tree, and no open integrity finding
// blocking the plane.
//
// The caller keeps the transaction open across its store delete and stamps
// the row in it: a claimant waits on the lifecycle row until the delete and
// the stamp commit together, and then finds the generation reclaimed. A
// claimant that arrived first holds the row, and this returns ErrConflict:
// "not yet", for the next pass.
func (repository *HangarOutputRepository) HoldForReclaim(ctx context.Context, tx output.Tx, ref hangar.TreeRef, grace time.Duration) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if grace <= 0 {
		return fmt.Errorf("%w: reclaim names no publication grace, and elapsed grace is one of its "+
			"preconditions", output.ErrIncomplete)
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	})
	if err != nil {
		return err
	}
	lifecycle, err := locks.LifecycleID(ref)
	if err != nil {
		return err
	}

	var reclaimed, withinGrace, atRisk bool
	var claims, pending int
	if err := hangarQueryRow(ctx, tx, `
		SELECT l.reclaimed_at IS NOT NULL,
		       l.registered_at > now() - $2::interval,
		       (SELECT count(*) FROM hangar_claims
		         WHERE lifecycle_id = l.id AND released_at IS NULL
		           AND (expires_at IS NULL OR expires_at > now())),
		       (SELECT count(*) FROM hangar_captures
		         WHERE scope = l.scope AND digest = l.digest AND state IN ('pending', 'publishing')) +
		       (SELECT count(*) FROM hangar_input_publications
		         WHERE scope = l.scope AND digest = l.digest AND lifecycle_id IS NULL AND expires_at > clock_timestamp()),
		       EXISTS (SELECT 1 FROM hangar_integrity_findings
		                WHERE resolved_at IS NULL
		                  AND violation IN ('out_of_band_absence', 'runtime_principal_denied'))
		FROM hangar_exact_lifecycles l WHERE l.id = $1`,
		[]any{lifecycle, hangarInterval(grace)},
		&reclaimed, &withinGrace, &claims, &pending, &atRisk); err != nil {
		return err
	}
	if reclaimed {
		return fmt.Errorf("%w: %s/%s/%d is already reclaimed", output.ErrConflict,
			ref.Scope, ref.Digest, ref.Generation)
	}
	if atRisk {
		return fmt.Errorf("%w: unresolved storage integrity findings; the reclaim pass deletes "+
			"nothing until an operator resolves them", output.ErrAtRisk)
	}
	if withinGrace {
		return fmt.Errorf("%w: %s/%s/%d is still inside its %s publication grace on the "+
			"database clock", output.ErrConflict, ref.Scope, ref.Digest, ref.Generation, grace)
	}
	if claims > 0 || pending > 0 {
		return fmt.Errorf("%w: %s/%s/%d is protected by %d live claim(s) and %d pending or "+
			"publishing capture(s) or unregistered input publication(s) naming the same tree",
			output.ErrConflict, ref.Scope, ref.Digest, ref.Generation, claims, pending)
	}

	return nil
}

// StampReclaimed records that the exact generation is gone from the store:
// the pass's delete was confirmed, found it already absent, or found another
// generation at its key. It is written in the transaction that held the row
// (HoldForReclaim) and asked the store, after the store answered.
//
// It enters the suffix itself rather than trusting that the caller did: the
// locks are already held by that transaction and are taken again for free,
// and a stamp written by any other transaction then takes them in the one
// order this plane has.
func (repository *HangarOutputRepository) StampReclaimed(ctx context.Context, tx output.Tx, ref hangar.TreeRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}

	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	})
	if err != nil {
		return err
	}
	lifecycle, err := locks.LifecycleID(ref)
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_exact_lifecycles SET reclaimed_at = now(), updated_at = now()
		WHERE id = $1 AND reclaimed_at IS NULL`, lifecycle)
	if err != nil {
		return hangarConflict(err)
	}
	stamped, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if stamped == 0 {
		return fmt.Errorf("%w: %s/%s/%d is already stamped reclaimed",
			output.ErrConflict, ref.Scope, ref.Digest, ref.Generation)
	}

	return nil
}

// RecordRuntimePrincipalDenial is the platform-principal mismatch in its
// runtime form.
//
// The IAM matrix says what the bucket's bindings CLAIM. This is the store
// saying otherwise: the web was refused while doing work its own role is
// supposed to authorize, which is either a warrant that was removed or a
// principal that is not the one the deployment configured. Either way the plane
// is not the plane that was configured, and carrying on admitting work under an
// identity that has just been refused is exactly the state an integrity
// finding exists to stop.
func (repository *HangarOutputRepository) RecordRuntimePrincipalDenial(ctx context.Context, tx output.Tx, role output.PrincipalRole, detail string) error {
	if err := role.Validate(); err != nil {
		return err
	}

	return repository.RecordRuntimeAtRisk(ctx, tx, output.IntegrityFindingRecord{
		Violation: output.ViolationRuntimePrincipalDenied,
		Subject:   string(role),
		Detail:    detail,
	})
}

// HangarDatabaseNow is the database clock, read as a value.
//
// Every deadline in this plane is measured against it rather than a node's own
// clock, and the orphan sweep that wants to ask "how old is this object" needs
// a reading it can compare with. The alternative -- time.Now() in a component
// -- is the one thing the database-clock rule exists to prevent.
func (repository *HangarOutputRepository) HangarDatabaseNow(ctx context.Context, tx output.Tx) (output.Timestamp, error) {
	var now time.Time
	if err := hangarQueryRow(ctx, tx, `SELECT now()`, nil, &now); err != nil {
		return output.Timestamp{}, err
	}

	return output.NewTimestamp(now.UTC()), nil
}
