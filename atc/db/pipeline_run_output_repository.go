package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// RunOutputVerifier verifies node observations against the deployment's
// epoch-pinned public keys. It performs no network or credential operations.
type RunOutputVerifier interface {
	VerifyCapture(output.CaptureAcknowledgement) error
	VerifyExecution(executioncontrol.Acknowledgement) error
	VerifyRelease(output.ReleaseAcknowledgement) error
}

// RunOutputRepository composes Run-owned producer decisions with the generic
// Hangar repository in the caller's transaction.
type RunReceiptVerifier interface {
	VerifySignature(output.Receipt) error
}

type RunOutputRepository struct {
	*HangarOutputRepository
	verifier RunOutputVerifier
	receipts RunReceiptVerifier
}

func NewRunOutputRepository(repository *HangarOutputRepository, verifier RunOutputVerifier, receipts RunReceiptVerifier) *RunOutputRepository {
	return &RunOutputRepository{HangarOutputRepository: repository, verifier: verifier, receipts: receipts}
}

// lockRunOutputHandoff discovers ownership without a lock, then follows the
// existing post-creation order. The Run and the capture each carry their own
// epoch: the Run's activation marker must not have been downgraded below the
// Run's birth epoch, and the capture's Hangar epoch -- the one it was
// predeclared under, not the Run's -- must still be enabled or draining.
// Completion can recover an outgoing Hangar epoch after new captures move to
// another; it cannot revive one whose drain completed.
func lockRunOutputHandoff(ctx context.Context, tx output.Tx, handoff output.HandoffID) (Tx, *runOutputOwner, error) {
	var owner runOutputOwner
	err := hangarQueryRow(ctx, tx, `SELECT s.run_id,s.build_id,r.activation_epoch,h.activation_epoch,p.team_id
  FROM pipeline_run_output_starts s JOIN pipeline_runs r ON r.id=s.run_id
  JOIN hangar_handoff_predeclarations h ON h.handoff_id=s.handoff_id
  JOIN pipelines p ON p.id=r.template_pipeline_id WHERE s.handoff_id=$1`, []any{string(handoff)}, &owner.runID, &owner.buildID, &owner.runEpoch, &owner.epoch, &owner.teamID)
	if err == sql.ErrNoRows {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	runTx, ok := tx.(Tx)
	if !ok {
		return nil, nil, fmt.Errorf("%w: Run output requires a Run transaction", output.ErrIncomplete)
	}
	var id int
	if err := runTx.QueryRowContext(ctx, `SELECT id FROM teams WHERE id=$1 FOR SHARE`, owner.teamID).Scan(&id); err != nil {
		return nil, nil, err
	}
	var current int64
	if err := runTx.QueryRowContext(ctx, `SELECT epoch FROM pipeline_run_activation WHERE singleton FOR SHARE`).Scan(&current); err != nil {
		return nil, nil, err
	}
	if current < owner.runEpoch {
		return nil, nil, atc.ErrRunResultsUnavailable
	}
	ready, err := hangarLockRecoverableEpoch(ctx, tx, owner.epoch)
	if err != nil {
		return nil, nil, err
	}
	if !ready {
		return nil, nil, atc.ErrRunResultsUnavailable
	}
	run := &pipelineRun{}
	if err := scanPipelineRun(run, pipelineRunsQuery.Where(sq.Eq{"r.id": owner.runID}).Suffix("FOR NO KEY UPDATE OF r").RunWith(runTx).QueryRowContext(ctx)); err != nil {
		return nil, nil, err
	}
	if run.ActivationEpoch() != owner.runEpoch {
		return nil, nil, atc.ErrRunResultsUnavailable
	}
	if run.Status() != atc.RunStatusRunning {
		return nil, nil, ErrPipelineRunNotRunning
	}
	payload, found := run.InstancePipelineID()
	if !found {
		return nil, nil, ErrPipelineRunPayloadGone
	}
	if err := runTx.QueryRowContext(ctx, `SELECT id FROM pipelines WHERE id=$1 FOR NO KEY UPDATE`, payload).Scan(&id); err != nil {
		return nil, nil, err
	}
	var actualRun, pipeline int
	if err := runTx.QueryRowContext(ctx, `SELECT pipeline_run_id,pipeline_id,aborted,completed FROM builds WHERE id=$1 FOR UPDATE`, owner.buildID).Scan(&actualRun, &pipeline, &owner.aborted, &owner.completed); err != nil {
		return nil, nil, err
	}
	if actualRun != owner.runID || pipeline != payload {
		return nil, nil, fmt.Errorf("%w: producer build is no longer active", output.ErrInvalidIdentity)
	}
	owner.cancelled = run.CancellationRequested()
	return runTx, &owner, nil
}

type runOutputOwner struct {
	runID, buildID, teamID int
	// runEpoch is the Run's birth activation epoch; epoch is the Hangar
	// epoch the capture was predeclared under.
	runEpoch, epoch int64
	cancelled       bool
	aborted         bool
	completed       bool
}

func (repository *RunOutputRepository) AcknowledgeSourceHold(ctx context.Context, tx output.Tx, ack output.CaptureAcknowledgement) error {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, ack.HandoffID)
	if err != nil {
		return err
	}
	if owner == nil {
		return repository.HangarOutputRepository.AcknowledgeSourceHold(ctx, tx, ack)
	}
	if repository.verifier == nil {
		return fmt.Errorf("%w: no Run output control verifier", output.ErrIncomplete)
	}
	if err := repository.verifier.VerifyCapture(ack); err != nil {
		return err
	}
	record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, ack.HandoffID)
	if err != nil {
		return err
	}
	if ack.Execution != record.Execution || ack.ActivationEpoch != record.ActivationEpoch || ack.SourceHoldID != record.SourceHoldID || ack.Incarnation != record.Source.Incarnation || ack.NodeUID != record.Source.Incarnation.NodeUID || ack.PodUID == "" {
		return fmt.Errorf("%w: hold does not match the admitted Run source", output.ErrInvalidIdentity)
	}
	if err := repository.HangarOutputRepository.AcknowledgeSourceHold(ctx, tx, ack); err != nil {
		return err
	}
	body, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	if _, err := runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_holds(handoff_id,acknowledgement) VALUES($1,$2) ON CONFLICT(handoff_id) DO NOTHING`, string(ack.HandoffID), body); err != nil {
		return err
	}
	var matches bool
	if err := runTx.QueryRowContext(ctx, `SELECT acknowledgement=$2::jsonb FROM pipeline_run_output_holds WHERE handoff_id=$1`, string(ack.HandoffID), body).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return fmt.Errorf("%w: Run hold acknowledgement changed on replay", output.ErrConflict)
	}
	return nil
}

func (repository *RunOutputRepository) verifyRunFinish(ctx context.Context, tx Tx, record output.HandoffRecord, witness executioncontrol.Acknowledgement) error {
	if witness.Kind != executioncontrol.AcknowledgementFinish && witness.Kind != executioncontrol.AcknowledgementStop {
		return fmt.Errorf("%w: Run completion requires a finish or stop witness", output.ErrInvalidIdentity)
	}
	if repository.verifier == nil {
		return fmt.Errorf("%w: no Run output control verifier", output.ErrIncomplete)
	}
	if err := repository.verifier.VerifyExecution(witness); err != nil {
		return err
	}
	var body []byte
	if err := tx.QueryRowContext(ctx, `SELECT acknowledgement FROM pipeline_run_output_holds WHERE handoff_id=$1`, string(record.HandoffID)).Scan(&body); err != nil {
		return fmt.Errorf("%w: Run has no retained source hold: %v", output.ErrIncomplete, err)
	}
	var hold output.CaptureAcknowledgement
	if err := json.Unmarshal(body, &hold); err != nil {
		return err
	}
	if err := repository.verifier.VerifyCapture(hold); err != nil {
		return err
	}
	if !record.HoldAcknowledged || !record.Source.Reserved() || witness.Identity != record.Execution || witness.ActivationEpoch != record.ActivationEpoch || witness.NodeUID != record.Source.Incarnation.NodeUID || witness.PodUID == "" || witness.PodUID != hold.PodUID || hold.Incarnation != record.Source.Incarnation || hold.Execution != record.Execution || hold.HandoffID != record.HandoffID || hold.SourceHoldID != record.SourceHoldID || hold.ActivationEpoch != record.ActivationEpoch {
		return fmt.Errorf("%w: finish does not match the Run token and exact held source", output.ErrInvalidIdentity)
	}
	return nil
}

func (repository *RunOutputRepository) CommitCaptureReservation(ctx context.Context, tx output.Tx, d output.SuccessfulFinishDisposition) (output.ReservationID, error) {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, d.HandoffID)
	if err != nil {
		return "", err
	}
	if owner == nil {
		return repository.HangarOutputRepository.CommitCaptureReservation(ctx, tx, d)
	}
	if err := d.Validate(); err != nil {
		return "", err
	}
	record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, d.HandoffID)
	if err != nil {
		return "", err
	}
	if d.Execution != record.Execution || d.ActivationEpoch != record.ActivationEpoch || d.SourceHoldID != record.SourceHoldID || d.Output != record.Output || d.CaptureFence != 1 || !d.CaptureDeadline.Equal(record.CaptureDeadline.Time) {
		return "", fmt.Errorf("%w: finish selection differs from Run start admission", output.ErrInvalidIdentity)
	}
	if err := repository.verifyRunFinish(ctx, runTx, record, d.FinishAcknowledgement); err != nil {
		return "", err
	}
	body, err := json.Marshal(d)
	if err != nil {
		return "", err
	}
	reservation, found, err := readRunFinishRepeat(ctx, runTx, d.HandoffID, output.DispositionCapture, body)
	if err != nil || found {
		return reservation, err
	}
	if owner.cancelled || owner.aborted || owner.completed {
		return "", fmt.Errorf("%w: cancellation precedes Run capture selection", output.ErrConflict)
	}
	reservation, err = repository.HangarOutputRepository.CommitCaptureReservation(ctx, tx, d)
	if err != nil {
		return "", err
	}
	_, err = runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_finishes(handoff_id,disposition,decision,producer_checkpoint_id,reservation_id) VALUES($1,'capture',$2,$3,$4)`, string(d.HandoffID), body, string(d.ProducerCheckpointID), string(reservation))
	return reservation, err
}

func (repository *RunOutputRepository) RecordNoCaptureIntent(ctx context.Context, tx output.Tx, d output.NoCaptureDisposition) error {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, d.HandoffID)
	if err != nil {
		return err
	}
	if owner == nil {
		return repository.HangarOutputRepository.RecordNoCaptureIntent(ctx, tx, d)
	}
	if err := d.Validate(); err != nil {
		return err
	}
	record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, d.HandoffID)
	if err != nil {
		return err
	}
	if d.Execution != record.Execution || d.ActivationEpoch != record.ActivationEpoch || d.SourceHoldID != record.SourceHoldID {
		return fmt.Errorf("%w: no-capture differs from Run start admission", output.ErrInvalidIdentity)
	}
	if d.FinishAcknowledgement != nil {
		if err := repository.verifyRunFinish(ctx, runTx, record, *d.FinishAcknowledgement); err != nil {
			return err
		}
	} else {
		// A typed lost/unresolved reconciliation needs its own Run evidence path.
		// An arbitrary caller-supplied reason is not evidence that recovery ended.
		return fmt.Errorf("%w: Run finish reconciliation is not yet available", output.ErrIncomplete)
	}
	body, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, found, err := readRunFinishRepeat(ctx, runTx, d.HandoffID, output.DispositionNoCapture, body)
	if err != nil || found {
		return err
	}
	if owner.cancelled || owner.aborted || owner.completed {
		return fmt.Errorf("%w: cancellation precedes Run no-capture selection", output.ErrConflict)
	}
	if err := repository.HangarOutputRepository.RecordNoCaptureIntent(ctx, tx, d); err != nil {
		return err
	}
	_, err = runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_finishes(handoff_id,disposition,decision) VALUES($1,'no_capture',$2)`, string(d.HandoffID), body)
	return err
}

func readRunFinishRepeat(ctx context.Context, tx Tx, handoff output.HandoffID, branch output.Disposition, body []byte) (output.ReservationID, bool, error) {
	var actual string
	var reservation sql.NullString
	var matches bool
	err := tx.QueryRowContext(ctx, `SELECT disposition,reservation_id,decision=$2::jsonb FROM pipeline_run_output_finishes WHERE handoff_id=$1`, string(handoff), body).Scan(&actual, &reservation, &matches)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if actual != string(branch) || !matches {
		return "", true, fmt.Errorf("%w: Run finish decision changed on replay", output.ErrConflict)
	}
	return output.ReservationID(reservation.String), true, nil
}

// LoadHandoffRecord adds the Run's cancellation request to a generic recovery
// snapshot. Selection rechecks it under the Run/build locks before committing.
func (repository *RunOutputRepository) LoadHandoffRecord(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffRecord, error) {
	record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, handoff)
	if err != nil {
		return record, err
	}
	var aborted bool
	err = hangarQueryRow(ctx, tx, `SELECT coalesce(b.aborted,false) OR r.cancel_requested_at IS NOT NULL FROM pipeline_run_output_starts s JOIN pipeline_runs r ON r.id=s.run_id LEFT JOIN builds b ON b.id=s.build_id WHERE s.handoff_id=$1`, []any{string(handoff)}, &aborted)
	if err != nil && err != sql.ErrNoRows {
		return record, err
	}
	record.CancellationRequested = record.CancellationRequested || aborted
	return record, nil
}

func (repository *RunOutputRepository) CancelOrSettle(ctx context.Context, tx output.Tx, handoff output.HandoffID) (output.HandoffStatus, error) {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, handoff)
	if err != nil {
		return output.HandoffStatus{}, err
	}
	if owner == nil {
		return repository.HangarOutputRepository.CancelOrSettle(ctx, tx, handoff)
	}
	if !owner.cancelled && !owner.aborted {
		return output.HandoffStatus{}, fmt.Errorf("%w: Run producer has no committed cancellation request", output.ErrConflict)
	}
	var pendingSource bool
	if err := runTx.QueryRowContext(ctx, `SELECT s.source_requested_at IS NOT NULL AND h.reserved_at IS NULL
		FROM pipeline_run_output_starts s JOIN hangar_handoff_predeclarations h USING(handoff_id)
		WHERE s.handoff_id=$1`, string(handoff)).Scan(&pendingSource); err != nil {
		return output.HandoffStatus{}, err
	}
	if pendingSource {
		return output.HandoffStatus{}, atc.ErrRunOutputPending
	}
	current, err := repository.HangarOutputRepository.ClassifyHandoff(ctx, tx, handoff)
	if err != nil {
		return current, err
	}
	if current.Disposition == nil || *current.Disposition == output.DispositionPreReservationCancel {
		var proven bool
		if err := runTx.QueryRowContext(ctx, `SELECT reserved_at IS NULL OR EXISTS(
		 SELECT 1 FROM pipeline_run_output_cancellation_evidence e WHERE e.handoff_id=h.handoff_id)
		 FROM hangar_handoff_predeclarations h WHERE handoff_id=$1`, string(handoff)).Scan(&proven); err != nil {
			return current, err
		}
		if !proven {
			return current, atc.ErrRunOutputPending
		}
	}
	if current.Disposition != nil && *current.Disposition != output.DispositionCapture {
		// A no-capture or pre-reservation-cancel winner keeps its original release
		// intent. Cancellation cannot replace either immutable decision.
		return current, nil
	}
	status, err := repository.HangarOutputRepository.CancelOrSettle(ctx, tx, handoff)
	if err != nil {
		return status, err
	}
	if current.Disposition != nil {
		return status, nil
	}
	if status.Disposition == nil || *status.Disposition != output.DispositionPreReservationCancel {
		return status, fmt.Errorf("%w: cancellation did not select a disposition", output.ErrIncomplete)
	}
	body, err := json.Marshal(struct {
		HandoffID   output.HandoffID   `json:"handoff_id"`
		Disposition output.Disposition `json:"disposition"`
	}{handoff, *status.Disposition})
	if err != nil {
		return status, err
	}
	_, err = runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_finishes(handoff_id,disposition,decision) VALUES($1,'pre_reservation_cancel',$2)`, string(handoff), body)
	return status, err
}

func (repository *RunOutputRepository) AcknowledgeNoCaptureRelease(ctx context.Context, tx output.Tx, ack output.ReleaseAcknowledgement) error {
	return repository.acknowledgeRunRelease(ctx, tx, ack, output.DispositionNoCapture)
}
func (repository *RunOutputRepository) AcknowledgePreReservationCancelRelease(ctx context.Context, tx output.Tx, ack output.ReleaseAcknowledgement) error {
	return repository.acknowledgeRunRelease(ctx, tx, ack, output.DispositionPreReservationCancel)
}
func (repository *RunOutputRepository) AcknowledgeCaptureRelease(ctx context.Context, tx output.Tx, ack output.ReleaseAcknowledgement) error {
	return repository.acknowledgeRunRelease(ctx, tx, ack, output.DispositionCapture)
}

func (repository *RunOutputRepository) acknowledgeRunRelease(ctx context.Context, tx output.Tx, ack output.ReleaseAcknowledgement, branch output.Disposition) error {
	runTx, owner, err := lockRunOutputHandoff(ctx, tx, ack.HandoffID)
	if err != nil {
		return err
	}
	if owner != nil {
		if repository.verifier == nil {
			return fmt.Errorf("%w: no Run output control verifier", output.ErrIncomplete)
		}
		if err := repository.verifier.VerifyRelease(ack); err != nil {
			return err
		}
		record, err := repository.HangarOutputRepository.LoadHandoffRecord(ctx, tx, ack.HandoffID)
		if err != nil {
			return err
		}
		if record.Disposition == nil || *record.Disposition != branch || ack.Disposition != branch || ack.Execution != record.Execution || ack.ActivationEpoch != record.ActivationEpoch || ack.SourceHoldID != record.SourceHoldID || ack.ReleaseIntentID != record.ReleaseIntentID || ack.Incarnation != record.Source.Incarnation {
			return fmt.Errorf("%w: release does not match the Run's exact fenced source", output.ErrInvalidIdentity)
		}
		body, err := json.Marshal(ack)
		if err != nil {
			return err
		}
		var matches bool
		err = runTx.QueryRowContext(ctx, `SELECT acknowledgement=$2::jsonb FROM pipeline_run_output_releases WHERE handoff_id=$1`, string(ack.HandoffID), body).Scan(&matches)
		if err == nil {
			if !matches {
				return fmt.Errorf("%w: Run source release changed on replay", output.ErrConflict)
			}
			return nil
		}
		if err != sql.ErrNoRows {
			return err
		}
		if owner.completed {
			return fmt.Errorf("%w: producer completed before release acknowledgement", output.ErrConflict)
		}
		if branch == output.DispositionCapture && record.State == output.CaptureStateRegistered {
			if err := repository.closeRunCapture(ctx, runTx, owner, record); err != nil {
				return err
			}
		}
		if _, err := runTx.ExecContext(ctx, `INSERT INTO pipeline_run_output_releases(handoff_id,acknowledgement) VALUES($1,$2)`, string(ack.HandoffID), body); err != nil {
			return err
		}
	}
	switch branch {
	case output.DispositionNoCapture:
		return repository.HangarOutputRepository.AcknowledgeNoCaptureRelease(ctx, tx, ack)
	case output.DispositionPreReservationCancel:
		return repository.HangarOutputRepository.AcknowledgePreReservationCancelRelease(ctx, tx, ack)
	case output.DispositionCapture:
		return repository.HangarOutputRepository.AcknowledgeCaptureRelease(ctx, tx, ack)
	}
	return fmt.Errorf("%w: unknown Run release disposition", output.ErrUnknownMember)
}
