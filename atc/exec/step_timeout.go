package exec

import (
	"context"
	"errors"
)

// A step timed out when its OWN context's deadline passed, and only then.
//
// A context.DeadlineExceeded inside a step's error is not evidence of that: a
// runtime may bound its own calls -- a ledger read, a witness write -- with a
// budget whose deadline is not the step's, and a step that failed on one would
// be deciding an outcome nobody proved. So the step's context is asked, never
// the error. A step whose context was cancelled is aborted, whatever deadline
// its error also carries.

// stepTimedOut reports whether ctx -- the context the step ran under, with its
// timeout applied -- is the thing that expired.
func stepTimedOut(ctx context.Context) bool {
	return errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// stepTimeoutError marks an error raised after the step's own timeout expired,
// for a step whose timeout context is narrower than the one that reports it.
type stepTimeoutError struct{ err error }

func (e stepTimeoutError) Error() string { return e.err.Error() }
func (e stepTimeoutError) Unwrap() error { return e.err }

// attributeStepTimeout marks err as the step's own timeout when ctx, the
// context carrying that timeout, expired.
func attributeStepTimeout(ctx context.Context, err error) error {
	if err != nil && stepTimedOut(ctx) {
		return stepTimeoutError{err: err}
	}
	return err
}

func isStepTimeout(err error) bool {
	var timedOut stepTimeoutError
	return errors.As(err, &timedOut)
}
