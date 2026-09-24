package db

import (
	"context"
	"errors"
	"fmt"
)

// ErrRunActivationEpochRequired refuses to create a Run born under no epoch.
var ErrRunActivationEpochRequired = errors.New("a pipeline run is born under a positive activation epoch")

// RunActivationEpochRegressedError refuses a configured Run activation epoch
// older than the one already recorded. The marker only moves forward: an
// older epoch is a downgraded control plane, and requirement 4 says that
// fails closed rather than admitting under stale capability.
type RunActivationEpochRegressedError struct {
	Recorded, Configured int64
}

func (e RunActivationEpochRegressedError) Error() string {
	return fmt.Sprintf("pipeline run activation epoch %d is older than the recorded epoch %d; configure epoch %d or later", e.Configured, e.Recorded, e.Recorded)
}

// RunActivation is the durable Run activation marker after reconciliation.
type RunActivation struct {
	Epoch            int64
	AdmissionEnabled bool
}

// ReconcilePipelineRunActivation makes the Run contract's own activation
// marker agree with this control plane's configuration, which is the one
// supported way the marker moves. A positive epoch admits new Runs born under
// it; zero stops admission and keeps the recorded epoch, so the marker never
// moves backwards and Runs already running continue. A configured epoch older
// than the recorded one is refused.
//
// The marker is independent of the Hangar output epoch (durable Run contract,
// amendment M-2 decision 3): rotating Hangar changes which epoch new captures
// are started under, never a Run's birth epoch, its finalization or the
// replay of its invocation key.
//
// Every web node runs this at startup. A node configured off disables
// admission for all of them, so a mixed fleet fails closed.
func ReconcilePipelineRunActivation(ctx context.Context, conn DbConn, epoch int64) (RunActivation, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return RunActivation{}, err
	}
	defer Rollback(tx)

	var state RunActivation
	if err := tx.QueryRowContext(ctx, `SELECT epoch, admission_enabled FROM pipeline_run_activation WHERE singleton FOR UPDATE`).
		Scan(&state.Epoch, &state.AdmissionEnabled); err != nil {
		return RunActivation{}, err
	}

	switch {
	case epoch <= 0:
		if state.AdmissionEnabled {
			if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_activation SET admission_enabled = false WHERE singleton`); err != nil {
				return RunActivation{}, err
			}
			state.AdmissionEnabled = false
		}
	case epoch < state.Epoch:
		return RunActivation{}, RunActivationEpochRegressedError{Recorded: state.Epoch, Configured: epoch}
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE pipeline_run_activation SET epoch = $1, admission_enabled = true WHERE singleton`, epoch); err != nil {
			return RunActivation{}, err
		}
		state.Epoch, state.AdmissionEnabled = epoch, true
	}

	return state, tx.Commit()
}
