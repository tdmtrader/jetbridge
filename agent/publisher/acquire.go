package publisher

import (
	"context"
	"fmt"
	"time"
)

const publicationAcquirePollInterval = 100 * time.Millisecond

// acquireForExecution waits behind an in-flight semantic operation instead of
// turning provider deduplication into a failure in the second workflow. Every
// pass through Store.Acquire re-authorizes and projects the caller's durable
// occurrence; after lease expiry exactly one waiter reclaims execution.
func acquireForExecution(
	ctx context.Context,
	store Store,
	request Request,
	lease time.Duration,
) (Publication, bool, error) {
	for {
		publication, execute, err := store.Acquire(ctx, request, lease)
		if err != nil || execute || publication.Status.terminal() {
			return publication, execute, err
		}
		if publication.Status != StatusPending || publication.LeaseUntil.IsZero() {
			return Publication{}, false, fmt.Errorf("%w: pending publication lease is invalid", ErrInvalidRequest)
		}
		wait := publicationAcquirePollInterval
		untilLeaseExpiry := time.Until(publication.LeaseUntil)
		if untilLeaseExpiry < wait {
			wait = untilLeaseExpiry
		}
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return Publication{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}
