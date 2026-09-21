package hangar

// The output read profile: the ONE way the foundation materializer is extended
// for a managed output.
//
// Requirement 59 and the plan's Green box both say the same thing in different
// words -- strict input v1 warrants and ordinary inputs remain unchanged. So this
// file adds no branch to Materialize. `MaterializeManaged` is a second entry
// point that wraps the SAME call with a profile's admission, renewal and
// release; the strict-input path does not know it exists, and materializer.go
// has no `if managed`.
//
// That is the whole of the byte-identical claim, and it is structural rather
// than measured: `Materialize` is the same function it was, and
// `MaterializeManaged` calls it. A test that materialized the same tree both
// ways and compared the trees would be a weaker statement -- it would prove the
// two agreed on one input rather than that one path was untouched.
//
// WHAT A PROFILE MAY DO. Admit before anything is opened, be renewed while work
// proceeds, and be released after the staging is verified. It cannot choose the
// destination, the ref or the storage path: those are the materializer's, and a
// profile that could move one would be a caller-chosen path by another name.

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// OutputReadProfile is the managed-output half of a materialization.
//
// Admit runs BEFORE the object is opened and reports how long the reader may
// work; a profile that cannot prove its authority returns an error and nothing
// is opened. Renew runs while work proceeds. Release runs after verified
// staging, and it runs on the failure path too -- protection nobody is using is
// protection that has to be given back.
type OutputReadProfile interface {
	Admit(ctx context.Context, ref TreeRef, handle, volume string) (time.Duration, error)
	Renew(ctx context.Context) error
	Release(ctx context.Context, staged error) error
}

// MaterializeManaged stages one exact managed-output generation under a profile.
//
// The ordering is the requirement: admitted first, opened second, released last,
// and the release happens whatever the staging did.
func (materializer *Materializer) MaterializeManaged(ctx context.Context, ref TreeRef, handle, volume string, profile OutputReadProfile) (err error) {
	if profile == nil {
		return fmt.Errorf("hangar: a managed-output materialization needs its read profile; an "+
			"output read is authorized by a committed lease and never by a warrant alone: %w",
			ErrUnauthorized)
	}
	if err := ref.Validate(); err != nil {
		return err
	}

	work, err := profile.Admit(ctx, ref, handle, volume)
	if err != nil {
		return err
	}
	if work <= 0 {
		return fmt.Errorf("hangar: the read profile admitted no working time for %s/%s/%d: %w",
			ref.Scope, ref.Digest, ref.Generation, ErrUnauthorized)
	}

	// The deadline is the profile's answer, not the caller's opinion. A reader
	// that outran its lease would be reading under authority the control plane
	// had already given away.
	staging, cancel := context.WithTimeout(ctx, work)
	defer cancel()

	staged := materializer.Materialize(staging, ref, handle, volume)

	// Released whatever happened, and the release's own failure is joined
	// rather than replacing the staging's: a caller that saw only "release
	// failed" would not know whether it had bytes.
	return errors.Join(staged, profile.Release(ctx, staged))
}

// RenewWhile keeps a profile's authority current for the length of a piece of
// work.
//
// It is here rather than inside MaterializeManaged because renewal is a
// question of how long the work runs, and the only caller who knows that is the
// one doing it. A materialization bounded by the admitted term needs no
// renewal; a longer transfer asks for one.
func RenewWhile(ctx context.Context, profile OutputReadProfile, every time.Duration, work func(context.Context) error) error {
	if profile == nil || every <= 0 {
		return fmt.Errorf("hangar: renewing needs a profile and a positive interval: %w",
			ErrUnauthorized)
	}

	renewing, stop := context.WithCancel(ctx)
	defer stop()

	finished := make(chan error, 1)
	go func() {
		var failure error
		defer func() { finished <- failure }()
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-renewing.Done():
				return
			case <-ticker.C:
				if err := profile.Renew(renewing); err != nil {
					// Cancellation after the work finishes is normal. A refusal
					// while it is running revokes the work's authority immediately.
					if renewing.Err() == nil {
						failure = err
						stop()
					}
					return
				}
			}
		}
	}()

	done := work(renewing)
	stop()

	// Join the renewer before returning so no renewal outlives the work it
	// protects and any failure that stopped the work reaches the caller.
	return errors.Join(done, <-finished)
}
