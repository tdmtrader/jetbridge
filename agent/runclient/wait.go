package runclient

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/atc"
)

// Wait polls a Run until it is no longer running. The platform exposes no
// completion hook outside the ATC, so polling is the contract. The caller's
// context bounds the wait; on expiry the last observation is returned with the
// context's error.
func (c *Client) Wait(ctx context.Context, handle Handle, every time.Duration) (RunObservation, error) {
	if every <= 0 {
		return RunObservation{}, errors.New("a positive poll interval is required")
	}
	for {
		run, err := c.Observe(ctx, handle)
		if err != nil {
			return run, err
		}
		if run.Status != atc.RunStatusRunning {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-time.After(every):
		}
	}
}

// Settled is the status a Run that is no longer running settled on: its
// terminal result's status once it has one.
func (o RunObservation) Settled() atc.RunStatus {
	if o.Terminal != nil {
		return o.Terminal.Status
	}
	return o.Status
}

// Succeeded reports whether the Run settled as succeeded.
func (o RunObservation) Succeeded() bool { return o.Settled() == atc.RunStatusSucceeded }
