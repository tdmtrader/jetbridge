package db

// The output plane's in-service flag, its integrity findings and its residue:
// what admission takes FOR SHARE, what an operator resolves, and what a drain
// waits on.

import (
	"context"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar/output"
)

// SetHangarEnabled moves the in-service row to the web's configured value and
// reports whether it moved.
//
// FOR UPDATE, so the move waits for every admission already holding the row
// FOR SHARE, and every admission after it reads the new value. It is the web's
// startup write: hangarOutput.webEnabled in the chart, rendered into the flag.
func SetHangarEnabled(ctx context.Context, conn DbConn, enabled bool) (bool, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)

	moved, err := hangarSetEnabled(ctx, tx, enabled)
	if err != nil {
		return false, err
	}

	return moved, tx.Commit()
}

// RecordRuntimeAtRisk preserves a storage failure until explicit operator
// resolution. Only the two runtime classes are recorded here; both block new
// admission (hangar_check_policy_admission) until an operator resolves them.
func (repository *HangarOutputRepository) RecordRuntimeAtRisk(ctx context.Context, tx output.Tx, finding output.PolicyFinding) error {
	if err := finding.Validate(); err != nil {
		return err
	}
	switch finding.Violation {
	case output.ViolationOutOfBandAbsence, output.ViolationRuntimePrincipalDenied:
	default:
		return fmt.Errorf("%w: %q is an attestation's finding and this is a runtime "+
			"observation", output.ErrConflict, finding.Violation)
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
		// Not parsed against the runtime vocabulary: a historical finding of
		// an attestation class is still an open row an operator resolves.
		finding.Violation = output.PolicyViolation(violation)
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
// was fixed" from "this was fixed, unfixed, and marked fixed again". A finding
// already resolved, or no finding at all, is ErrNotFound.
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
		return fmt.Errorf("%w: no open integrity finding %d", output.ErrNotFound, id)
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
