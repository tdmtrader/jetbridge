package db

import (
	"context"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/concourse/concourse/atc"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrPipelineRunCancelling              = errors.New("pipeline run cancellation has been requested")
	ErrPipelineRunCancellationUnsupported = errors.New("pipeline run contract does not support cancellation")
	ErrPipelineRunCancelReason            = errors.New("cancellation reason must be 1 to 512 UTF-8 bytes without control characters")
)

// AcceptRunCancellation only records the command. Its caller commits before
// stopping executors or settling sources. The immutable birth epoch already
// identifies the request's Run/Hangar generation; no credential is stored here.
// This operation needs only the shared Run boundary, and acquires no inner locks.
func (f *pipelineRunFactory) AcceptRunCancellation(ctx context.Context, tx Tx, runID int, requester string, reason *string) (atc.RunCancelOutcome, error) {
	return acceptRunCancellation(ctx, tx, runID, requester, reason)
}

// AbortedBuildCancellationRequester is who asks for a Run's cancellation when
// one of its aborted builds cannot finish over an execution nothing else will
// close (build.finish).
const AbortedBuildCancellationRequester = "system:aborted-build"

func acceptRunCancellation(ctx context.Context, tx Tx, runID int, requester string, reason *string) (atc.RunCancelOutcome, error) {
	if requester == "" {
		return "", errors.New("cancellation requires an authenticated requester")
	}
	var err error
	reason, err = NormalizeRunCancellationReason(reason)
	if err != nil {
		return "", err
	}
	run, err := lockPipelineRun(tx, runID)
	if err != nil {
		return "", err
	}
	if run.ContractVersion() != atc.RunContractV2 {
		return "", ErrPipelineRunCancellationUnsupported
	}
	// Request metadata wins over terminal status, including a lost-response retry
	// after convergence has published the aborted outcome.
	if run.CancellationRequested() {
		return atc.RunCancelAlreadyRequested, nil
	}
	if run.Status() != atc.RunStatusRunning {
		return atc.RunCancelAlreadyTerminal, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE pipeline_runs SET cancel_requested_at=clock_timestamp(),cancel_requested_by=$2,cancel_reason=$3 WHERE id=$1`, runID, requester, reason)
	if err != nil {
		return "", err
	}
	_, err = tx.ExecContext(ctx, `SELECT pg_notify($1,''),pg_notify($2,'')`, atc.ComponentHangarOutputCapture, atc.ComponentRunCancellation)
	return atc.RunCancelAccepted, err
}

// NormalizeRunCancellationReason is shared by the closed HTTP decoder and the
// internal command so malformed requests fail before lookup without echoing text.
func NormalizeRunCancellationReason(reason *string) (*string, error) {
	if reason != nil {
		if !utf8.ValidString(*reason) {
			return nil, ErrPipelineRunCancelReason
		}
		normalized := norm.NFC.String(*reason)
		if len(normalized) < 1 || len(normalized) > 512 {
			return nil, ErrPipelineRunCancelReason
		}
		for _, r := range normalized {
			if unicode.IsControl(r) {
				return nil, ErrPipelineRunCancelReason
			}
		}
		reason = &normalized
	}

	return reason, nil
}

func (f *pipelineRunFactory) RequestRunCancellation(ctx context.Context, runID int, requester string, reason *string) (atc.RunCancelOutcome, error) {
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer Rollback(tx)
	outcome, err := f.AcceptRunCancellation(ctx, tx, runID, requester, reason)
	if err != nil {
		return "", err
	}
	return outcome, tx.Commit()
}
