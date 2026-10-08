package db

import (
	"context"

	"github.com/concourse/concourse/atc"
)

// The Run contract has its own activation marker, independent of the Hangar
// output plane. Every Run transaction locks it first, before any Run-domain
// lock; a transaction that also needs the output plane locks its in-service
// row next, still inside the activation prefix.

// runActivationMarker is the Run activation marker as a transaction locked it.
type runActivationMarker struct {
	epoch   int64
	enabled bool
}

// admits is admission's test: the marker must admit, at exactly the epoch the
// new Run is born under.
func (m runActivationMarker) admits(epoch int64) error {
	if epoch <= 0 || !m.enabled || m.epoch != epoch {
		return atc.ErrRunResultsUnavailable
	}
	return nil
}

// continues is the test for work on a Run that already exists: the marker
// must not have been downgraded below the Run's birth epoch. It need not
// admit -- turning admission off stops new Runs, not running ones -- and it is
// indifferent to the output plane.
func (m runActivationMarker) continues(runEpoch int64) error {
	if runEpoch <= 0 || m.epoch < runEpoch {
		return atc.ErrRunResultsUnavailable
	}
	return nil
}

// lockRunContinuation is the prefix for work on a Run that already exists.
func lockRunContinuation(ctx context.Context, tx Tx, runEpoch int64) error {
	marker, err := lockRunActivationMarker(ctx, tx)
	if err != nil {
		return err
	}
	return marker.continues(runEpoch)
}

func lockRunActivationMarker(ctx context.Context, tx Tx) (runActivationMarker, error) {
	var marker runActivationMarker
	err := tx.QueryRowContext(ctx, `SELECT epoch, admission_enabled FROM pipeline_run_activation WHERE singleton FOR SHARE`).Scan(&marker.epoch, &marker.enabled)
	return marker, err
}

// lockEnabledHangarOutput requires the output plane to be in service:
// hangar_enabled, taken FOR SHARE. A caller with no plane never reaches it.
func lockEnabledHangarOutput(ctx context.Context, tx Tx) error {
	ready, err := hangarLockEnabled(ctx, tx)
	if err != nil {
		return err
	}
	if !ready {
		return atc.ErrRunResultsUnavailable
	}
	return nil
}
