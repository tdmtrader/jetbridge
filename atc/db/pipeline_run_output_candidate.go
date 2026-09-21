package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
)

// closeRunCapture runs under the owning Run boundary and before capture
// settlement. A cancelled producer discards its output; a live producer retains
// a hidden candidate whose public eligibility is decided at terminal publication.
func (repository *RunOutputRepository) closeRunCapture(ctx context.Context, tx Tx, owner *runOutputOwner, record output.HandoffRecord) error {
	if owner.cancelled || owner.aborted {
		reason := "build_aborted"
		if owner.cancelled {
			reason = "run_cancelled"
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO pipeline_run_output_discards(handoff_id,reason) VALUES($1,$2)`, string(record.HandoffID), reason)
		return err
	}
	if record.Receipt == nil || repository.receipts == nil {
		return fmt.Errorf("%w: Run candidate needs verified receipt evidence", output.ErrIncomplete)
	}
	if err := repository.receipts.VerifySignature(*record.Receipt); err != nil {
		return err
	}
	c := record.Receipt.Claims
	if c.Execution != record.Execution || c.ActivationEpoch != record.ActivationEpoch || c.HandoffID != record.HandoffID || c.ProducerCheckpointID != record.ProducerCheckpointID || c.ReservationID != record.ReservationID || c.Output != record.Output || c.Incarnation != record.Source.Incarnation || c.CaptureFence != record.CaptureFence || c.WriterFence != output.FirstWriterFence || c.Ref != record.Ref {
		return fmt.Errorf("%w: receipt does not belong to this Run producer", output.ErrInvalidIdentity)
	}
	var matches bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pipeline_run_output_finishes
		WHERE handoff_id=$1 AND disposition='capture' AND reservation_id=$2 AND producer_checkpoint_id=$3)`, string(record.HandoffID), string(record.ReservationID), string(record.ProducerCheckpointID)).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("%w: candidate has no Run completion checkpoint", output.ErrInvalidIdentity)
	}
	claim := output.ClaimID(uuid.NewString())
	// Collect the complete suffix before entering either the claim or capture
	// operation. Those operations re-lock only rows already acquired here.
	if _, err := LockHangarSuffix(ctx, tx, repository.prefix, HangarLockRequest{
		Logical: []HangarLogicalKey{{Scope: record.Ref.Scope, Digest: record.Ref.Digest}},
		Exact:   []hangar.TreeRef{record.Ref}, Captures: []output.ReservationID{record.ReservationID},
		Receipts: []output.ReservationID{record.ReservationID}, Claims: []output.ClaimID{claim},
	}); err != nil {
		return err
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT now()`).Scan(&now); err != nil {
		return err
	}
	if err := repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
		ProtocolVersion: output.ProtocolVersion, ClaimID: claim, Ref: record.Ref,
		ConsumerBindingID: output.OpaqueID(record.HandoffID), RequestedAt: output.NewTimestamp(now),
	}); err != nil {
		return err
	}
	body, err := json.Marshal(record.Receipt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pipeline_run_output_candidates
		(handoff_id,claim_id,scope,digest,generation,receipt) VALUES($1,$2,$3,$4,$5,$6)`,
		string(record.HandoffID), string(claim), string(record.Ref.Scope), string(record.Ref.Digest), record.Ref.Generation, body)
	return err
}
