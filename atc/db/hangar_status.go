package db

// The output plane's in-service flag, its integrity findings and its residue:
// what admission takes FOR SHARE, what an operator resolves, and what a drain
// waits on.

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
)

// SetHangarEnabled moves the in-service row to the given value and reports
// whether it moved.
func SetHangarEnabled(ctx context.Context, conn DbConn, enabled bool) (bool, error) {
	_, moved, err := ReconcileHangarEnabled(ctx, conn, enabled)
	return moved, err
}

// ReconcileHangarEnabled is the web's startup write: hangarOutput.webEnabled,
// rendered into the flag. It reads the row without a lock first and writes
// only when the configured value differs, so a web restart that changes
// nothing takes no lock admission would wait on. It returns the value it
// found and whether it moved the row.
//
// A move takes the row FOR UPDATE, so it waits for every admission already
// holding it FOR SHARE, and every admission after it reads the new value.
func ReconcileHangarEnabled(ctx context.Context, conn DbConn, enabled bool) (bool, bool, error) {
	var current bool
	err := conn.QueryRowContext(ctx, `SELECT enabled FROM hangar_enabled WHERE singleton`).Scan(&current)
	found := err == nil
	if err != nil && err != sql.ErrNoRows {
		return false, false, err
	}
	if found && current == enabled {
		return current, false, nil
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return current, false, err
	}
	defer Rollback(tx)

	moved, err := hangarSetEnabled(ctx, tx, enabled)
	if err != nil {
		return current, false, err
	}

	return current, moved, tx.Commit()
}

// RecordRuntimeAtRisk preserves a storage failure until explicit operator
// resolution. Only the two runtime classes are recorded here; both block new
// admission (hangar_check_policy_admission) until an operator resolves them.
func (repository *HangarOutputRepository) RecordRuntimeAtRisk(ctx context.Context, tx output.Tx, finding output.IntegrityFindingRecord) error {
	if err := finding.Validate(); err != nil {
		return err
	}
	switch finding.Violation {
	case output.ViolationOutOfBandAbsence, output.ViolationRuntimePrincipalDenied:
	default:
		return fmt.Errorf("%w: %q is not a runtime "+
			"integrity finding", output.ErrConflict, finding.Violation)
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hangar_integrity_findings (violation, subject, detail)
		VALUES ($1, $2, $3)
		ON CONFLICT (violation, subject) WHERE resolved_at IS NULL
		DO NOTHING`,
		string(finding.Violation), finding.Subject, finding.Detail); err != nil {
		return hangarConflict(err)
	}

	return nil
}

// OpenIntegrityFindings reads every unresolved finding, oldest first.
func (repository *HangarOutputRepository) OpenIntegrityFindings(ctx context.Context, tx output.Tx) ([]output.IntegrityFinding, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, violation, subject, detail, observed_at,
		       violation IN ('out_of_band_absence', 'runtime_principal_denied')
		  FROM hangar_integrity_findings
		 WHERE resolved_at IS NULL
		 ORDER BY observed_at, id`)
	if err != nil {
		return nil, hangarConflict(err)
	}
	defer Close(rows)

	findings := []output.IntegrityFinding{}
	for rows.Next() {
		var finding output.IntegrityFinding
		var violation string
		var observed time.Time
		if err := rows.Scan(&finding.ID, &violation, &finding.Subject, &finding.Detail,
			&observed, &finding.BlocksAdmission); err != nil {
			return nil, err
		}
		// Not parsed against the runtime vocabulary: a historical finding of a
		// class that no longer exists is still an open row an operator resolves.
		finding.Violation = output.IntegrityViolation(violation)
		finding.ObservedAt = output.NewTimestamp(observed.UTC())
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, hangarConflict(err)
	}

	return findings, nil
}

// ResolveIntegrityFinding closes one finding by id: the operator's explicit
// statement that its cause was repaired.
//
// It is one-way and the schema says so: a reopened finding is a resolution that
// never happened, and an audit reading these rows has to be able to tell "this
// was fixed" from "this was fixed, unfixed, and marked fixed again". Resolving
// a finding already resolved succeeds and changes nothing; no finding at all
// is ErrNotFound.
func (repository *HangarOutputRepository) ResolveIntegrityFinding(ctx context.Context, tx output.Tx, id int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE hangar_integrity_findings SET resolved_at = now()
		 WHERE id = $1 AND resolved_at IS NULL`, id)
	if err != nil {
		return hangarConflict(err)
	}
	closed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if closed == 0 {
		// Already resolved is resolved: the operator's statement stands, and
		// a retry of it succeeds. No finding at all is not found.
		var exists int
		if err := hangarQueryRow(ctx, tx, `SELECT count(*) FROM hangar_integrity_findings WHERE id = $1`,
			[]any{id}, &exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("%w: no integrity finding %d", output.ErrNotFound, id)
		}
	}

	return nil
}

// CountOutputPlaneState counts what this plane is holding, in ONE statement:
// the residue a drain waits on, beside the live generations it keeps.
//
// One statement because the numbers are compared with each other: a plane with
// six live generations and six open claims is a plane doing its job, and a plane
// with six live generations and one open claim from a build that finished
// yesterday is a leak. Counting them at different instants would let an
// operator draw a conclusion about a state that never existed.
func (repository *HangarOutputRepository) CountOutputPlaneState(ctx context.Context, tx output.Tx) (output.PlaneCounts, error) {
	var counts output.PlaneCounts
	rows, err := tx.QueryContext(ctx, `
		SELECT
			(SELECT count(*) FROM hangar_exact_lifecycles
			  WHERE state IN ('registered', 'adopted', 'reclaiming')),
			(SELECT count(*) FROM hangar_captures WHERE state = 'pending'),
			(SELECT count(*) FROM hangar_captures WHERE state = 'publishing'),
			(SELECT count(*) FROM hangar_captures
			  WHERE released_at IS NULL AND state IN ('published', 'discarded', 'failed')),
			(SELECT count(*) FROM hangar_captures WHERE release_unacknowledged),
			(SELECT count(*) FROM hangar_claims WHERE released_at IS NULL),
			(SELECT count(*) FROM hangar_read_leases
			  WHERE released_at IS NULL AND expires_at > now()),
			(SELECT count(*) FROM hangar_reclaim_jobs WHERE finalized_at IS NULL),
			(SELECT count(*) FROM hangar_integrity_findings WHERE resolved_at IS NULL)`)
	if err != nil {
		return output.PlaneCounts{}, hangarConflict(err)
	}
	defer Close(rows)

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return output.PlaneCounts{}, hangarConflict(err)
		}

		return output.PlaneCounts{}, fmt.Errorf("%w: counting the output plane returned no row",
			output.ErrCorrupt)
	}
	if err := rows.Scan(&counts.LiveGenerations, &counts.PendingCaptures, &counts.PublishingCaptures,
		&counts.UnreleasedCaptures, &counts.UnacknowledgedReleases, &counts.OpenClaims, &counts.OpenReadLeases,
		&counts.UnfinalizedReclaimJobs, &counts.OpenIntegrityFindings); err != nil {
		return output.PlaneCounts{}, hangarConflict(err)
	}
	counts.NonterminalCaptures = counts.PendingCaptures + counts.PublishingCaptures

	return counts, nil
}

// HangarEnabled reads the in-service flag without locking it: the status
// surface reports it, and admission takes it FOR SHARE on its own path.
func (repository *HangarOutputRepository) HangarEnabled(ctx context.Context, tx output.Tx) (bool, error) {
	var enabled bool
	if err := hangarQueryRow(ctx, tx, `SELECT enabled FROM hangar_enabled WHERE singleton`, nil, &enabled); err != nil {
		return false, err
	}

	return enabled, nil
}

// HangarAbsences records a registered generation found missing by a reader,
// in a transaction of its own.
type HangarAbsences struct {
	Conn DbConn
}

// RecordUnexpectedAbsence moves a REGISTERED or ADOPTED generation to
// missing_out_of_band and records the blocking finding. A generation being
// reclaimed, already reclaimed or already missing is not news and is left
// alone: its absence is explained by this plane's own delete.
func (absences HangarAbsences) RecordUnexpectedAbsence(ctx context.Context, ref hangar.TreeRef) error {
	tx, err := absences.Conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)

	repository := NewHangarOutputRepository(HangarConsumerPrefixForComponent())
	recorded, err := repository.recordUnexpectedAbsence(ctx, tx, ref)
	if err != nil || !recorded {
		return err
	}

	return HangarCommitError(tx.Commit())
}

func (repository *HangarOutputRepository) recordUnexpectedAbsence(ctx context.Context, tx output.Tx, ref hangar.TreeRef) (bool, error) {
	if err := ref.Validate(); err != nil {
		return false, err
	}
	locks, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: ref.Scope, Digest: ref.Digest}},
		Exact:   []hangar.TreeRef{ref},
	})
	if err != nil {
		return false, err
	}
	id, registered := locks.Lifecycles[ref]
	if !registered {
		return false, nil
	}
	var live bool
	if err := hangarQueryRow(ctx, tx, `SELECT state IN ('registered', 'adopted') FROM hangar_exact_lifecycles WHERE id = $1`,
		[]any{id}, &live); err != nil {
		return false, err
	}
	if !live {
		return false, nil
	}

	return true, repository.RecordOutOfBandAbsence(ctx, tx, ref)
}
