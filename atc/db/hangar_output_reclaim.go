package db

// The reclaim job's durable half: admission's fenced lease, the admitted-delete
// record that has to exist BEFORE any external call, the outcome that record
// later carries, and the finalization that says which of four things happened.
//
// Two rules run through all of it.
//
// The first is that the record precedes the effect. An admitted delete is
// written and committed before the reclaimer is allowed to ask the object store
// to delete anything, because the alternative -- ask first, record after -- has
// a window in which the object is gone and nothing durable says this system
// asked for it. Absence with no prior admitted delete is an out-of-band
// lifetime violation, and a plane that could not tell the two apart would
// quietly rewrite one as the other.
//
// The second is that the fence, not the expiry, is what authorizes a write. An
// owner whose lease expired may have been taken over, and a takeover advances
// the fence; every statement here names the fence it believes it holds, so a
// paused owner that wakes up writes nothing.
//
// One recorded imprecision, ruled on rather than fixed (round 1, R1-F14). Every
// lease instant here is now(), which in PostgreSQL is TRANSACTION START -- so a
// transaction open for N seconds overstates its remaining lease by N. The exact
// form is clock_timestamp(), which hangarStatProofFresh already uses for the
// reason it matters there. It is not used here because consistency across the
// plane is worth more than the milliseconds: these transactions are short, the
// same reading is what every earlier phase's lease arithmetic uses, and
// tightening one site alone would make two kinds of lease mean two things.
// Revisit if a lease is ever tightened below a minute.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// HangarReclaimJob is one admitted reclamation.
//
// It carries the exact ref and metageneration the delete will be conditioned
// on, read back from the row rather than from the caller: a job whose
// precondition came from the process that is about to delete would be a
// precondition that process chose.
type HangarReclaimJob struct {
	ID              int64
	LifecycleID     int64
	Ref             hangar.TreeRef
	Metageneration  int64
	ActivationEpoch int64
	OwnerID         string
	LeaseFence      output.LeaseFence
	RenewedAt       output.Timestamp
	ExpiresAt       output.Timestamp
	AdmittedDeletes int
}

// Deferred: the operator status surface reports the reclaim backlog as a
// count, because a series per in-flight object is cardinality nobody can
// alert on. Loading one job and reading its remaining term is a diagnosis
// of a SPECIFIC object, and this track ships no API that names one
//
// Remaining is how much of the lease is left at a database-clock instant.
func (job HangarReclaimJob) Remaining(now output.Timestamp) time.Duration {
	return job.ExpiresAt.UTC().Sub(now.UTC())
}

// Deferred: the operator status surface reports the reclaim backlog as a
// count, because a series per in-flight object is cardinality nobody can
// alert on. Loading one job and reading its remaining term is a diagnosis
// of a SPECIFIC object, and this track ships no API that names one
//
// LoadReclaimJob reads the open job for one exact ref.
func (repository *HangarOutputRepository) LoadReclaimJob(ctx context.Context, tx output.Tx, ref hangar.TreeRef) (HangarReclaimJob, error) {
	if err := ref.Validate(); err != nil {
		return HangarReclaimJob{}, err
	}

	job := HangarReclaimJob{Ref: ref}
	var renewedAt, expiresAt time.Time
	err := hangarQueryRow(ctx, tx, `
		SELECT j.id, j.lifecycle_id, j.metageneration, j.activation_epoch, j.owner_id,
		       j.lease_fence, j.renewed_at, j.expires_at,
		       (SELECT count(*) FROM hangar_reclaim_attempts a WHERE a.job_id = j.id)
		  FROM hangar_reclaim_jobs j
		  JOIN hangar_exact_lifecycles l ON l.id = j.lifecycle_id
		 WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3
		   AND j.finalized_at IS NULL`,
		[]any{string(ref.Scope), string(ref.Digest), ref.Generation},
		&job.ID, &job.LifecycleID, &job.Metageneration, &job.ActivationEpoch, &job.OwnerID,
		&job.LeaseFence, &renewedAt, &expiresAt, &job.AdmittedDeletes)
	if errors.Is(err, output.ErrNotFound) {
		return HangarReclaimJob{}, fmt.Errorf("%w: no open reclaim job for %s/%s/%d",
			output.ErrNotFound, ref.Scope, ref.Digest, ref.Generation)
	}
	if err != nil {
		return HangarReclaimJob{}, err
	}
	job.RenewedAt = output.NewTimestamp(renewedAt.UTC())
	job.ExpiresAt = output.NewTimestamp(expiresAt.UTC())

	return job, nil
}

// RenewReclaimLease extends a reclaim lease its owner still holds.
//
// The fence is named as well as the owner, and the expiry is checked on the
// database clock, so an owner that was taken over -- or one that let its lease
// lapse and is asking for it back -- is refused rather than resurrected. A
// delete admitted under a lapsed lease is a delete under authority somebody
// else now holds.
func (repository *HangarOutputRepository) RenewReclaimLease(ctx context.Context, tx output.Tx, job HangarReclaimJob, term time.Duration) (HangarReclaimJob, error) {
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return HangarReclaimJob{}, err
	}

	renewed := job
	var renewedAt, expiresAt time.Time
	err = hangarQueryRow(ctx, tx, `
		UPDATE hangar_reclaim_jobs
		   SET renewed_at = now(), expires_at = now() + $4::interval
		 WHERE id = $1 AND owner_id = $2 AND lease_fence = $3
		   AND finalized_at IS NULL AND expires_at > now()
		RETURNING renewed_at, expires_at`,
		[]any{job.ID, job.OwnerID, int64(job.LeaseFence), interval}, &renewedAt, &expiresAt)
	if errors.Is(err, output.ErrNotFound) {
		return HangarReclaimJob{}, fmt.Errorf("%w: reclaim job %d is no longer owner %s at fence "+
			"%d, or it has expired or finalized; an expired owner cannot delete or finalize",
			output.ErrConflict, job.ID, job.OwnerID, job.LeaseFence)
	}
	if err != nil {
		return HangarReclaimJob{}, err
	}
	renewed.RenewedAt = output.NewTimestamp(renewedAt.UTC())
	renewed.ExpiresAt = output.NewTimestamp(expiresAt.UTC())

	return renewed, nil
}

// TakeOverReclaimJob advances the fence of an expired job to a new owner.
//
// Expiry alone releases nothing and proves nothing; what it permits is this.
// The advance is what makes the previous owner's later writes -- including a
// delete outcome it is still holding -- refuse.
func (repository *HangarOutputRepository) TakeOverReclaimJob(ctx context.Context, tx output.Tx, job HangarReclaimJob, owner string, term time.Duration) (HangarReclaimJob, error) {
	interval, err := hangarLeaseInterval(term)
	if err != nil {
		return HangarReclaimJob{}, err
	}
	if owner == "" {
		return HangarReclaimJob{}, fmt.Errorf("%w: a reclaim takeover names its owner",
			output.ErrIncomplete)
	}

	taken := job
	taken.OwnerID = owner
	var renewedAt, expiresAt time.Time
	err = hangarQueryRow(ctx, tx, `
		UPDATE hangar_reclaim_jobs
		   SET owner_id = $2, lease_fence = lease_fence + 1,
		       renewed_at = now(), expires_at = now() + $3::interval
		 WHERE id = $1 AND finalized_at IS NULL AND expires_at <= now()
		RETURNING lease_fence, renewed_at, expires_at`,
		[]any{job.ID, owner, interval}, &taken.LeaseFence, &renewedAt, &expiresAt)
	if errors.Is(err, output.ErrNotFound) {
		return HangarReclaimJob{}, fmt.Errorf("%w: reclaim job %d has not expired on the database "+
			"clock, or it is already finalized; a live owner is not taken over",
			output.ErrConflict, job.ID)
	}
	if err != nil {
		return HangarReclaimJob{}, err
	}
	taken.RenewedAt = output.NewTimestamp(renewedAt.UTC())
	taken.ExpiresAt = output.NewTimestamp(expiresAt.UTC())

	return taken, nil
}

// AdmitDelete writes the durable record that this system is about to ask the
// object store to delete an exact generation.
//
// It is a separate call from the delete, and the caller must COMMIT it before
// making the external one. That ordering is the whole reason the record exists:
// after it, an object found absent with a lost response is `reclaimed_inferred`,
// and an object found absent with no such record is an out-of-band lifetime
// violation. Ask-then-record would make the second indistinguishable from the
// first for the width of the window.
//
// It also enforces Req 48's start condition: work begins only with the delete
// timeout plus two minutes of lease remaining, measured on the database clock,
// because a delete that starts with less has no way to finish inside its own
// authority.
func (repository *HangarOutputRepository) AdmitDelete(ctx context.Context, tx output.Tx, job HangarReclaimJob, deleteTimeout time.Duration) (int64, error) {
	if deleteTimeout <= 0 {
		return 0, fmt.Errorf("%w: an admitted delete names no timeout, and the lease it must "+
			"finish inside is derived from one", output.ErrIncomplete)
	}

	// Both instants come from the DATABASE, and the subtraction is done here.
	//
	// Not `extract(epoch FROM (expires_at - now()))`, which would be one
	// statement instead of two values -- but `FROM` inside `extract` is the one
	// place this plane's own table-naming guard cannot tell a keyword from a
	// table, and a rule that has to be taught an exception is a rule with an
	// exception. Reading two timestamps and subtracting them is the same
	// arithmetic on the same clock.
	var expiresAt, databaseNow time.Time
	if err := hangarQueryRow(ctx, tx, `
		SELECT expires_at, now() FROM hangar_reclaim_jobs
		 WHERE id = $1 AND owner_id = $2 AND lease_fence = $3 AND finalized_at IS NULL`,
		[]any{job.ID, job.OwnerID, int64(job.LeaseFence)}, &expiresAt, &databaseNow); err != nil {
		if errors.Is(err, output.ErrNotFound) {
			return 0, fmt.Errorf("%w: reclaim job %d is no longer owner %s at fence %d, or it is "+
				"finalized", output.ErrConflict, job.ID, job.OwnerID, job.LeaseFence)
		}

		return 0, err
	}
	remaining := expiresAt.Sub(databaseNow)
	if !output.MayStartWork(remaining, deleteTimeout) {
		return 0, fmt.Errorf("%w: reclaim job %d has %s of lease left and a delete of %s needs "+
			"%s; work begins only with the delete timeout plus %s remaining",
			output.ErrTimeout, job.ID, remaining.Round(time.Second), deleteTimeout,
			(deleteTimeout + output.LeaseStartMargin), output.LeaseStartMargin)
	}

	var attempt int64
	if err := hangarQueryRow(ctx, tx, `
		INSERT INTO hangar_reclaim_attempts (job_id, lease_fence)
		VALUES ($1, $2)
		RETURNING id`, []any{job.ID, int64(job.LeaseFence)}, &attempt); err != nil {
		return 0, err
	}

	return attempt, nil
}

// RecordDeleteOutcome attaches what the object store answered to one admitted
// delete.
//
// Every member of the vocabulary is recorded, including the failures, because
// the caller's next move differs per outcome and an error alone cannot say
// which: `already_absent` finalizes, `generation_conflict` becomes debt and
// never broadens into an unconditional delete, and a timeout is an answer
// nobody got rather than an object nobody deleted.
//
// It is fenced. An owner that was taken over while its delete was in flight
// records nothing, which is what stops two owners writing two answers about one
// attempt.
func (repository *HangarOutputRepository) RecordDeleteOutcome(ctx context.Context, tx output.Tx, job HangarReclaimJob, attempt int64, outcome output.DeleteOutcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_reclaim_attempts a
		   SET outcome = $3, observed_at = now()
		 WHERE a.id = $1 AND a.lease_fence = $2 AND a.outcome IS NULL
		   AND EXISTS (
		       SELECT 1 FROM hangar_reclaim_jobs j
		        WHERE j.id = a.job_id AND j.owner_id = $4 AND j.lease_fence = $2
		          AND j.finalized_at IS NULL)`,
		attempt, int64(job.LeaseFence), string(outcome), job.OwnerID)
	if err != nil {
		return hangarConflict(err)
	}
	recorded, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if recorded == 0 {
		return fmt.Errorf("%w: attempt %d already carries an outcome, or reclaim job %d is no "+
			"longer owner %s at fence %d; an expired owner cannot delete or finalize",
			output.ErrConflict, attempt, job.ID, job.OwnerID, job.LeaseFence)
	}

	return nil
}

// FinalizeReclaim closes a job and says which of four things happened.
//
// It writes the lifecycle state as well, in the same transaction, so a
// generation can never be `reclaimed_confirmed` in one table and `reclaiming`
// in the other. The schema's own evidence trigger is what refuses a claim this
// job's attempts do not support -- confirmed with no acknowledged delete,
// inferred with no admitted delete or no observed absence -- and that check is
// deliberately not repeated here: two copies of an evidence rule are two
// chances for one of them to be the one that was not updated.
func (repository *HangarOutputRepository) FinalizeReclaim(ctx context.Context, tx output.Tx, job HangarReclaimJob, outcome output.ReclaimOutcome, absenceObserved bool) error {
	if err := outcome.Validate(); err != nil {
		return err
	}

	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: job.Ref.Scope, Digest: job.Ref.Digest}},
		Exact:   []hangar.TreeRef{job.Ref},
	}); err != nil {
		return err
	}

	absence := "absence_observed_at"
	if absenceObserved {
		absence = "coalesce(absence_observed_at, now())"
	}
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE hangar_reclaim_jobs
		   SET outcome = $3, finalized_at = now(), absence_observed_at = %s
		 WHERE id = $1 AND lease_fence = $2 AND finalized_at IS NULL
		   AND expires_at > now()`, absence),
		job.ID, int64(job.LeaseFence), string(outcome))
	if err != nil {
		return hangarConflict(err)
	}
	finalized, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if finalized == 0 {
		return fmt.Errorf("%w: reclaim job %d is already finalized, or its lease has expired or "+
			"been taken over at fence %d; an expired owner cannot finalize",
			output.ErrConflict, job.ID, job.LeaseFence)
	}

	// And the lifecycle row, in the same transaction. `conflicted` and
	// `abandoned` are different: the first is a generation this plane found
	// something else at, and the second is a job given up on, whose generation
	// goes back to being protected rather than being deleted on a guess.
	state := string(outcome)
	if outcome == output.ReclaimAbandoned {
		state = ""
	}
	if state != "" {
		if _, err := tx.ExecContext(ctx, `
			UPDATE hangar_exact_lifecycles SET state = $2, updated_at = now() WHERE id = $1`,
			job.LifecycleID, state); err != nil {
			return hangarConflict(err)
		}

		return nil
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_exact_lifecycles SET state = origin, updated_at = now()
		WHERE id = $1 AND state = 'reclaiming'`, job.LifecycleID); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// RecordOutOfBandAbsence is what an exact generation's disappearance means when
// no admitted delete explains it.
//
// It is a LIFETIME VIOLATION and never a reclamation, and the two are kept apart
// here because rewriting one as the other is exactly how a bucket losing objects
// to somebody else's lifecycle rule would look like this system working
// correctly. The state it writes is terminal in its own right: `reclaiming` is
// not a prerequisite, because nothing admitted this.
func (repository *HangarOutputRepository) RecordOutOfBandAbsence(ctx context.Context, tx output.Tx, ref hangar.TreeRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}

	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	}); err != nil {
		return err
	}

	var admitted int
	if err := hangarQueryRow(ctx, tx, `
		SELECT count(*)
		  FROM hangar_reclaim_attempts a
		  JOIN hangar_reclaim_jobs j ON j.id = a.job_id
		  JOIN hangar_exact_lifecycles l ON l.id = j.lifecycle_id
		 WHERE l.scope = $1 AND l.digest = $2 AND l.generation = $3`,
		[]any{string(ref.Scope), string(ref.Digest), ref.Generation}, &admitted); err != nil {
		return err
	}
	if admitted > 0 {
		return fmt.Errorf("%w: %s/%s/%d has %d admitted delete(s) on record; its absence is this "+
			"plane's reclamation to finalize, not an out-of-band violation to report",
			output.ErrConflict, ref.Scope, ref.Digest, ref.Generation, admitted)
	}

	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_exact_lifecycles
		   SET state = 'missing_out_of_band', updated_at = now()
		 WHERE scope = $1 AND digest = $2 AND generation = $3
		   AND state NOT IN ('reclaimed_confirmed', 'reclaimed_inferred', 'missing_out_of_band')`,
		string(ref.Scope), string(ref.Digest), ref.Generation)
	if err != nil {
		return hangarConflict(err)
	}
	recorded, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if recorded == 0 {
		return fmt.Errorf("%w: %s/%s/%d has no lifecycle row this plane may move, or it is "+
			"already terminal", output.ErrConflict, ref.Scope, ref.Digest, ref.Generation)
	}

	// And the epoch, because Req 52 says an unexpected exact absence enters
	// durable at-risk state and blocks new admissions from detection onward.
	// Moving one lifecycle row and stopping -- which is what this did -- says
	// one object is gone while the plane carries on publishing into a bucket
	// something else is deleting from.
	var epoch int64
	if err := hangarQueryRow(ctx, tx, `
		SELECT activation_epoch FROM hangar_exact_lifecycles
		 WHERE scope = $1 AND digest = $2 AND generation = $3`,
		[]any{string(ref.Scope), string(ref.Digest), ref.Generation}, &epoch); err != nil {
		return err
	}

	return repository.RecordRuntimeAtRisk(ctx, tx, epoch, output.PolicyFinding{
		Violation: output.ViolationOutOfBandAbsence,
		Subject:   fmt.Sprintf("%s/%s/%d", ref.Scope, ref.Digest, ref.Generation),
		Detail: "the exact generation is absent from the output bucket and no admitted delete " +
			"on record explains it; this is a lifetime violation and never a reclamation",
	})
}

// RecordRuntimePrincipalDenial is Req 52's platform-principal mismatch in its
// runtime form.
//
// The IAM matrix says what the bucket's bindings CLAIM. This is the store
// saying otherwise: a controller was refused while doing work its own role is
// supposed to authorize, which is either a grant that was removed or a
// principal that is not the one the deployment configured. Either way the plane
// is not the plane that was attested, and carrying on admitting work under an
// identity that has just been refused is exactly the state Req 52 stops.
func (repository *HangarOutputRepository) RecordRuntimePrincipalDenial(ctx context.Context, tx output.Tx, epoch int64, role output.PrincipalRole, detail string) error {
	if err := role.Validate(); err != nil {
		return err
	}

	return repository.RecordRuntimeAtRisk(ctx, tx, epoch, output.PolicyFinding{
		Violation: output.ViolationRuntimePrincipalDenied,
		Subject:   string(role),
		Detail:    detail,
	})
}

// DueReclaimJobs is the reclaimer's bounded work query: open jobs whose leases
// this owner may act under, oldest first.
func (repository *HangarOutputRepository) DueReclaimJobs(ctx context.Context, tx output.Tx, limit int) ([]HangarReclaimJob, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a reclaim pass is bounded; %d is not a batch",
			output.ErrIncomplete, limit)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT j.id, j.lifecycle_id, l.scope, l.digest, l.generation, j.metageneration,
		       j.activation_epoch, j.owner_id, j.lease_fence, j.renewed_at, j.expires_at,
		       (SELECT count(*) FROM hangar_reclaim_attempts a WHERE a.job_id = j.id)
		  FROM hangar_reclaim_jobs j
		  JOIN hangar_exact_lifecycles l ON l.id = j.lifecycle_id
		 WHERE j.finalized_at IS NULL
		 ORDER BY j.admitted_at, j.id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var jobs []HangarReclaimJob
	for rows.Next() {
		var job HangarReclaimJob
		var scope, digest string
		var renewedAt, expiresAt time.Time
		if err := rows.Scan(&job.ID, &job.LifecycleID, &scope, &digest, &job.Ref.Generation,
			&job.Metageneration, &job.ActivationEpoch, &job.OwnerID, &job.LeaseFence,
			&renewedAt, &expiresAt, &job.AdmittedDeletes); err != nil {
			return nil, err
		}
		job.Ref.Scope = hangar.Scope(scope)
		job.Ref.Digest = hangar.Digest(digest)
		job.RenewedAt = output.NewTimestamp(renewedAt.UTC())
		job.ExpiresAt = output.NewTimestamp(expiresAt.UTC())
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return jobs, nil
}

// HangarDatabaseNow is the database clock, read as a value.
//
// Every deadline in this plane is measured against it rather than a node's own
// clock, and a controller that wants to ask "how much of my lease is left"
// needs a reading it can compare with. The alternative -- time.Now() in a
// controller -- is the one thing the lease design exists to prevent.
func (repository *HangarOutputRepository) HangarDatabaseNow(ctx context.Context, tx output.Tx) (output.Timestamp, error) {
	var now time.Time
	if err := hangarQueryRow(ctx, tx, `SELECT now()`, nil, &now); err != nil {
		return output.Timestamp{}, err
	}

	return output.NewTimestamp(now.UTC()), nil
}

// HangarReclaimCandidate is one generation whose reclamation may be admitted.
//
// It is what the admission pass selects and nothing it decides: every exclusion
// named here is rechecked by AdmitReclaim under the exact-lifecycle lock, and
// the schema rechecks the policy half again at commit. Selecting on them here as
// well is not a second copy of the rule -- it is the bound that stops a pass
// from opening a transaction per protected generation in the deployment.
type HangarReclaimCandidate struct {
	Ref            hangar.TreeRef
	Metageneration int64
	RegisteredAt   output.Timestamp
}

// ReclaimCandidates is the admission pass's bounded work query.
//
// The grace comparison is made on the DATABASE clock against the row's own
// registered_at, for the same reason every deadline in this plane is: a
// controller that measured grace on its own clock could admit a delete for an
// object whose grace had not elapsed by simply being wrong about the time.
//
// registered_at is the instant this plane learned the generation exists, and it
// is deliberately used rather than the object's creation time, which this table
// does not carry. For a `registered` row the create precedes the receipt, and
// for an `adopted` row adoption itself already required grace to have elapsed
// since the object was created. Both directions are therefore conservative: the
// wait is never shorter than grace measured from creation.
func (repository *HangarOutputRepository) ReclaimCandidates(ctx context.Context, tx output.Tx, epoch int64, grace time.Duration, limit int) ([]HangarReclaimCandidate, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a reclaim admission pass is bounded; %d is not a batch",
			output.ErrIncomplete, limit)
	}
	if grace <= 0 {
		return nil, fmt.Errorf("%w: a reclaim admission pass names no publication grace, and "+
			"elapsed grace is one of Req 46's preconditions", output.ErrIncomplete)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT l.scope, l.digest, l.generation, l.metageneration, l.registered_at
		  FROM hangar_exact_lifecycles l
		 WHERE l.activation_epoch = $1
		   AND l.state IN ('registered', 'adopted')
		   AND l.registered_at <= now() - $2::interval
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_claims c
		        WHERE c.lifecycle_id = l.id AND c.released_at IS NULL)
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_read_leases r
		        WHERE r.lifecycle_id = l.id AND r.released_at IS NULL AND r.expires_at > now())
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_logical_reservations g
		        WHERE g.scope = l.scope AND g.digest = l.digest
		          AND g.state = 'unresolved_generation')
		   AND NOT EXISTS (
		       SELECT 1 FROM hangar_reclaim_jobs j
		        WHERE j.lifecycle_id = l.id AND j.finalized_at IS NULL)
		 ORDER BY l.registered_at, l.id
		 LIMIT $3`, epoch, hangarInterval(grace), limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var candidates []HangarReclaimCandidate
	for rows.Next() {
		var candidate HangarReclaimCandidate
		var scope, digest string
		var registeredAt time.Time
		if err := rows.Scan(&scope, &digest, &candidate.Ref.Generation,
			&candidate.Metageneration, &registeredAt); err != nil {
			return nil, err
		}
		candidate.Ref.Scope = hangar.Scope(scope)
		candidate.Ref.Digest = hangar.Digest(digest)
		candidate.RegisteredAt = output.NewTimestamp(registeredAt.UTC())
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return candidates, nil
}

// LifetimeAuditCandidates is the absence reconciliation's bounded work query:
// the generations this plane believes exist, least recently confirmed first.
//
// It is the other half of Req 52's "unexpected exact absence". The inventory
// sweep classifies objects it SEES; nothing in a listing can report an object
// that is not there, so a registered generation that somebody else's lifecycle
// rule removed is invisible to it forever. This asks the opposite question --
// of the objects this plane says exist, which ones does the store not have --
// and it is the only question whose answer can be an out-of-band violation.
func (repository *HangarOutputRepository) LifetimeAuditCandidates(ctx context.Context, tx output.Tx, epoch int64, limit int) ([]hangar.TreeRef, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: a lifetime audit is bounded; %d is not a batch",
			output.ErrIncomplete, limit)
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT l.scope, l.digest, l.generation
		  FROM hangar_exact_lifecycles l
		 WHERE l.activation_epoch = $1
		   AND l.state IN ('registered', 'adopted')
		 ORDER BY coalesce(l.lifetime_audited_at, l.registered_at), l.id
		 LIMIT $2`, epoch, limit)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	var refs []hangar.TreeRef
	for rows.Next() {
		var ref hangar.TreeRef
		var scope, digest string
		if err := rows.Scan(&scope, &digest, &ref.Generation); err != nil {
			return nil, err
		}
		ref.Scope = hangar.Scope(scope)
		ref.Digest = hangar.Digest(digest)
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return refs, nil
}

// RecordLifetimePresence stamps a generation the audit statted and found.
//
// Presence is recorded and not only absence, because the audit has to make
// progress: a pass that stamped nothing would re-stat the same oldest rows every
// wake and never reach the rest of the bucket. It writes no state, because
// finding an object where this plane said it was is not news.
func (repository *HangarOutputRepository) RecordLifetimePresence(ctx context.Context, tx output.Tx, ref hangar.TreeRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	// The exact class, named rather than taken by the UPDATE below: see
	// RecordFirstObjectCreate for why an unnamed single-class lock is still a
	// lock this order has to be able to see.
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Exact: []hangar.TreeRef{ref},
	}); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE hangar_exact_lifecycles SET lifetime_audited_at = now()
		 WHERE scope = $1 AND digest = $2 AND generation = $3`,
		string(ref.Scope), string(ref.Digest), ref.Generation); err != nil {
		return hangarConflict(err)
	}

	return nil
}
