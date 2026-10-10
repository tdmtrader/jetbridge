package db

import (
	"context"

	"github.com/concourse/concourse/atc"
)

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
