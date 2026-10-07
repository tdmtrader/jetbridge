package hangaroutput

// The capture sequence, driven by the control plane over one row.
//
//  1. step start      the consumer inserts a pending row; the step's control
//                     init writes the node's held marker before any container
//  2. step exit       the node's finish statement names the Pod; the daemon
//                     flips the marker to sealed, waits for every container of
//                     that Pod to terminate, canonicalizes, answers the digest
//  3. CAS             pending -> publishing, writing the digest BEFORE any
//                     object can exist (or pending -> discarded)
//  4. publish         the daemon creates the object for that digest, or joins
//                     the one this plane already marked with it
//  5. CAS             publishing -> published, writing the generation; the
//                     same transaction registers the lifecycle and takes the
//                     capture's claim
//  6. release         terminal rows with no released_at: the daemon clears the
//                     marker, then released_at is stamped; retried forever
//
// Recovery is the same sequence read from the row: a publishing row asks the
// store what it holds for its digest and either completes step 5 or repeats
// step 4, and fails when the store holds nothing and the sealed step is gone
// from its node; a pending row past its capture deadline fails, and so does a
// publishing row past its deadline plus PublishingMargin. A terminal row whose
// node is gone is released once FinishedAt is NodeGoneMargin old: the marker
// went with the node.
//
// The seal is asynchronous on the node: step 2 is asked again on each pass
// until it answers the digest, so no HTTP timeout bounds a slow Pod or a
// large tree. Pending rows are advanced concurrently, at most Concurrency at
// once.
//
// Two rules run through every method. No database lock is held across a
// network call: each step reads in one short transaction, closes it, asks the
// node, and commits the answer as one CAS in a second. And nothing is
// remembered between calls: a lost answer is resolved by reading the row
// again, never by a value kept in memory. Multi-web fencing beyond the CAS is
// out of scope -- a second coordinator races the same CAS and an idempotent
// create, and loses benignly.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
)

// DefaultBatchSize bounds each of a pass's queries.
const DefaultBatchSize = 100

// DefaultConcurrency is how many pending captures one pass advances at once.
const DefaultConcurrency = 8

// DefaultPublishingMargin is how long past its capture deadline a publishing
// row is recovered before it fails.
const DefaultPublishingMargin = time.Hour

// DefaultNodeGoneMargin is how long a terminal row on a node that no longer
// exists waits before it is released without the node's acknowledgement.
const DefaultNodeGoneMargin = time.Hour

// Coordinator advances capture rows. It holds no state about any capture.
type Coordinator struct {
	Transactor Transactor
	Rows       CaptureRows
	Dialer     SourceDialer

	// ActivationEpoch is the epoch a published generation's lifecycle and
	// claim are recorded under while epochs exist.
	ActivationEpoch executioncontrol.ActivationEpoch

	BatchSize int

	// Concurrency bounds the pending captures one pass advances at once.
	// Zero means DefaultConcurrency.
	Concurrency int

	// PublishingMargin and NodeGoneMargin bound recovery; zero means the
	// defaults.
	PublishingMargin time.Duration
	NodeGoneMargin   time.Duration

	// Now is the clock NodeGoneMargin is measured on; nil means time.Now.
	Now func() time.Time
}

func (coordinator *Coordinator) concurrency() int {
	if coordinator.Concurrency <= 0 {
		return DefaultConcurrency
	}

	return coordinator.Concurrency
}

func (coordinator *Coordinator) publishingMargin() time.Duration {
	if coordinator.PublishingMargin <= 0 {
		return DefaultPublishingMargin
	}

	return coordinator.PublishingMargin
}

func (coordinator *Coordinator) nodeGoneMargin() time.Duration {
	if coordinator.NodeGoneMargin <= 0 {
		return DefaultNodeGoneMargin
	}

	return coordinator.NodeGoneMargin
}

func (coordinator *Coordinator) now() time.Time {
	if coordinator.Now == nil {
		return time.Now()
	}

	return coordinator.Now()
}

func (coordinator *Coordinator) batch() int {
	if coordinator.BatchSize <= 0 {
		return DefaultBatchSize
	}

	return coordinator.BatchSize
}

// Insert is step 1's database half, for a consumer that does not compose it
// into a transaction of its own.
func (coordinator *Coordinator) Insert(ctx context.Context, pending output.PendingCapture) (output.Capture, error) {
	var capture output.Capture
	err := coordinator.write(func(tx Transaction) (err error) {
		capture, err = coordinator.Rows.InsertPending(ctx, tx, pending)
		return err
	})

	return capture, err
}

// Advance takes one capture as far as it can go now: through the seal, the
// publish and the release, stopping at the first step that has to wait.
func (coordinator *Coordinator) Advance(ctx context.Context, key output.CaptureKey) error {
	for range 4 {
		capture, err := coordinator.get(ctx, key)
		if err != nil {
			return err
		}
		progressed := false
		switch {
		case capture.State == output.CapturePending:
			progressed, err = coordinator.seal(ctx, capture)
		case capture.State == output.CapturePublishing:
			progressed, err = coordinator.publish(ctx, capture, false)
		case capture.State.Terminal() && !capture.Released():
			progressed, err = coordinator.release(ctx, capture)
		}
		if err != nil || !progressed {
			return err
		}
	}

	return nil
}

// Settle advances one capture until it is terminal and released, asking
// again while the node's asynchronous seal is in progress, the way successive
// passes do. It is for a caller that composes the sequence inline -- a test
// harness, a single-process runtime -- and is bounded by wait.
func (coordinator *Coordinator) Settle(ctx context.Context, key output.CaptureKey, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		if err := coordinator.Advance(ctx, key); err != nil {
			return err
		}
		capture, err := coordinator.get(ctx, key)
		if err != nil {
			return err
		}
		if capture.State.Terminal() && capture.Released() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: capture %s did not settle in %s; it is %s", output.ErrUnresolved,
				key, wait, capture.State)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Discard is the cancelled-or-no-output branch of step 3. A pending capture
// becomes discarded; a publishing or published one is past the point a
// cancellation may undo, and is reported as such rather than touched.
func (coordinator *Coordinator) Discard(ctx context.Context, key output.CaptureKey, reason string) (output.Capture, error) {
	var capture output.Capture
	err := coordinator.write(func(tx Transaction) (err error) {
		capture, err = coordinator.Rows.CASPendingToDiscarded(ctx, tx, key, reason)
		return err
	})
	if errors.Is(err, output.ErrConflict) {
		current, getErr := coordinator.get(ctx, key)
		if getErr != nil {
			return output.Capture{}, errors.Join(err, getErr)
		}

		return current, nil
	}

	return capture, err
}

// Run is one pass: every pending capture, recovery of every publishing one,
// the deadline for every pending one past it, and the release of every
// terminal one. A failure on one row is collected and never stops the others.
func (coordinator *Coordinator) Run(ctx context.Context) error {
	var errs []error
	var mu sync.Mutex
	collect := func(key output.CaptureKey, err error) {
		if err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("capture %s: %w", key, err))
			mu.Unlock()
		}
	}

	expired, err := coordinator.list(ctx, coordinator.Rows.ListPendingPastDeadline)
	if err != nil {
		return err
	}
	for _, capture := range expired {
		collect(capture.Key, coordinator.expire(ctx, capture))
	}

	stale, err := coordinator.list(ctx, func(ctx context.Context, tx output.Tx, limit int) ([]output.Capture, error) {
		return coordinator.Rows.ListPublishingPastDeadline(ctx, tx, coordinator.publishingMargin(), limit)
	})
	if err != nil {
		return err
	}
	for _, capture := range stale {
		_, err := coordinator.fail(ctx, capture.Key, "the capture deadline passed and recovery never "+
			"completed the publication")
		collect(capture.Key, err)
	}

	publishing, err := coordinator.list(ctx, coordinator.Rows.ListPublishingForRecovery)
	if err != nil {
		return err
	}
	for _, capture := range publishing {
		_, err := coordinator.publish(ctx, capture, true)
		collect(capture.Key, err)
	}

	pending, err := coordinator.list(ctx, coordinator.Rows.ListPending)
	if err != nil {
		return err
	}
	slots := make(chan struct{}, coordinator.concurrency())
	var wg sync.WaitGroup
	for _, capture := range pending {
		if ctx.Err() != nil {
			break
		}
		slots <- struct{}{}
		wg.Add(1)
		go func(key output.CaptureKey) {
			defer wg.Done()
			defer func() { <-slots }()
			collect(key, coordinator.Advance(ctx, key))
		}(capture.Key)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return errors.Join(append(errs, ctx.Err())...)
	}

	unreleased, err := coordinator.list(ctx, coordinator.Rows.ListUnreleased)
	if err != nil {
		return err
	}
	for _, capture := range unreleased {
		_, err := coordinator.release(ctx, capture)
		collect(capture.Key, err)
	}

	return errors.Join(errs...)
}

// seal is steps 2 and 3. It answers whether the row moved.
func (coordinator *Coordinator) seal(ctx context.Context, capture output.Capture) (bool, error) {
	node, err := coordinator.Dialer.ForNode(ctx, capture.Node, capture.NodeUID)
	if err != nil {
		return false, err
	}
	observed, err := node.Observe(ctx, capture.Execution, 0)
	if err != nil {
		return false, err
	}
	ack := observed.Acknowledgement
	if ack == nil || ack.Identity != capture.Execution {
		// Still running, or never started. The capture deadline is the bound.
		return false, nil
	}
	switch {
	case ack.Kind == executioncontrol.AcknowledgementStop:
		return coordinator.discard(ctx, capture.Key, output.DiscardProducerStopped)
	case ack.Kind != executioncontrol.AcknowledgementFinish:
		return false, nil
	case ack.Outcome == nil || !ack.Outcome.Successful():
		return coordinator.discard(ctx, capture.Key, output.DiscardProducerFailed)
	}

	sealed, err := node.Seal(ctx, output.CaptureSealRequest{
		ProtocolVersion: output.ProtocolVersion,
		Execution:       capture.Execution,
		Output:          capture.Key.Output,
		PodUID:          ack.PodUID,
	})
	switch {
	case errors.Is(err, output.ErrSealInProgress), errors.Is(err, output.ErrUnresolved),
		errors.Is(err, output.ErrSealUnconfirmed):
		// The node is waiting for the Pod's containers to stop, or reading the
		// tree, or a background seal ran out and the next ask starts another.
		// Ask again next pass; the capture deadline is the bound.
		return false, nil
	case errors.Is(err, output.ErrNotFound):
		// No held marker for this step on its node. Nothing is captured from
		// a directory nothing held, so the capture fails rather than
		// publishing what happens to be there.
		return coordinator.fail(ctx, capture.Key, "no held marker on the node: "+err.Error())
	case err != nil:
		return false, err
	}
	if err := sealed.Validate(); err != nil {
		return false, err
	}
	if sealed.Marker.Key() != capture.Key || sealed.Marker.PodUID != ack.PodUID {
		return false, fmt.Errorf("%w: the seal answered for %s in pod %s", output.ErrInvalidIdentity,
			sealed.Marker.Key(), sealed.Marker.PodUID)
	}

	err = coordinator.write(func(tx Transaction) error {
		_, err := coordinator.Rows.CASPendingToPublishing(ctx, tx, capture.Key, ack.PodUID,
			sealed.Scope, sealed.Digest)
		return err
	})

	return err == nil, err
}

// publish is steps 4 and 5. recovering asks the store first, so a row whose
// object was created before a lost answer completes onto that object rather
// than creating anything.
func (coordinator *Coordinator) publish(ctx context.Context, capture output.Capture, recovering bool) (bool, error) {
	node, err := coordinator.Dialer.ForNode(ctx, capture.Node, capture.NodeUID)
	if err != nil {
		return false, err
	}

	var result output.CapturePublishResult
	found := false
	if recovering {
		result, err = node.Stat(ctx, output.CaptureStatRequest{
			ProtocolVersion: output.ProtocolVersion, Execution: capture.Execution, Digest: capture.Digest,
		})
		switch {
		case err == nil:
			found = true
		case errors.Is(err, output.ErrNotFound):
		case errors.Is(err, output.ErrConflict):
			return coordinator.fail(ctx, capture.Key, "the object at this digest is not one this "+
				"plane marked with it: "+err.Error())
		default:
			return false, err
		}
	}
	if !found {
		result, err = node.Publish(ctx, output.CapturePublishRequest{
			ProtocolVersion: output.ProtocolVersion,
			Execution:       capture.Execution,
			Output:          capture.Key.Output,
			Digest:          capture.Digest,
		})
		switch {
		case errors.Is(err, output.ErrConflict):
			// A collision at the key, or a sealed tree that is no longer the
			// recorded digest. Never adopted, never overwritten.
			return coordinator.fail(ctx, capture.Key, "publish conflict: "+err.Error())
		case errors.Is(err, output.ErrNotFound):
			// Not in the store (recovery asked first, or this is the first
			// publish), and no sealed step to publish from: nothing can ever
			// create it now.
			return coordinator.fail(ctx, capture.Key, "the sealed step is gone from its node: "+err.Error())
		case err != nil:
			return false, err
		}
	}
	if err := result.Validate(); err != nil {
		return false, err
	}
	if result.Ref.Scope != capture.Scope || result.Ref.Digest != capture.Digest {
		return false, fmt.Errorf("%w: the store answered %s/%s for a capture of %s/%s",
			output.ErrInvalidIdentity, result.Ref.Scope, result.Ref.Digest, capture.Scope, capture.Digest)
	}

	err = coordinator.write(func(tx Transaction) error {
		_, err := coordinator.Rows.CASPublishingToPublished(ctx, tx, output.PublishedCapture{
			Key:             capture.Key,
			Generation:      result.Ref.Generation,
			Metageneration:  result.Metageneration,
			ActivationEpoch: coordinator.ActivationEpoch,
		})
		return err
	})

	return err == nil, err
}

// release is step 6.
func (coordinator *Coordinator) release(ctx context.Context, capture output.Capture) (bool, error) {
	node, err := coordinator.Dialer.ForNode(ctx, capture.Node, capture.NodeUID)
	if err != nil {
		if nodeGone(err) && capture.FinishedAt != nil &&
			coordinator.now().Sub(*capture.FinishedAt) >= coordinator.nodeGoneMargin() {
			// The node the marker lived on is gone or was replaced; the
			// marker went with it, and there is nothing left to clear.
			err = coordinator.write(func(tx Transaction) error {
				_, err := coordinator.Rows.SetReleased(ctx, tx, capture.Key)
				return err
			})

			return err == nil, err
		}

		return false, err
	}
	ack, err := node.Release(ctx, output.CaptureReleaseRequest{
		ProtocolVersion: output.ProtocolVersion, Execution: capture.Execution, Output: capture.Key.Output,
	})
	if err != nil {
		return false, err
	}
	if err := ack.Validate(); err != nil {
		return false, err
	}
	if ack.Key != capture.Key {
		return false, fmt.Errorf("%w: the node released %s for %s", output.ErrInvalidIdentity, ack.Key, capture.Key)
	}
	err = coordinator.write(func(tx Transaction) error {
		_, err := coordinator.Rows.SetReleased(ctx, tx, capture.Key)
		return err
	})

	return err == nil, err
}

// nodeGone is a dialer's answer that the node no longer exists, or that the
// name now belongs to a different node.
func nodeGone(err error) bool {
	return errors.Is(err, output.ErrNotFound) || errors.Is(err, output.ErrInvalidIdentity)
}

func (coordinator *Coordinator) expire(ctx context.Context, capture output.Capture) error {
	_, err := coordinator.fail(ctx, capture.Key, "the capture deadline passed before the step's "+
		"output was sealed")

	return err
}

func (coordinator *Coordinator) discard(ctx context.Context, key output.CaptureKey, reason string) (bool, error) {
	err := coordinator.write(func(tx Transaction) error {
		_, err := coordinator.Rows.CASPendingToDiscarded(ctx, tx, key, reason)
		return err
	})

	return err == nil, err
}

func (coordinator *Coordinator) fail(ctx context.Context, key output.CaptureKey, reason string) (bool, error) {
	err := coordinator.write(func(tx Transaction) error {
		_, err := coordinator.Rows.MarkFailed(ctx, tx, key, reason)
		return err
	})

	return err == nil, err
}

func (coordinator *Coordinator) get(ctx context.Context, key output.CaptureKey) (output.Capture, error) {
	tx, err := coordinator.Transactor.Begin()
	if err != nil {
		return output.Capture{}, err
	}
	defer tx.Rollback()

	return coordinator.Rows.GetCapture(ctx, tx, key)
}

func (coordinator *Coordinator) list(ctx context.Context,
	query func(context.Context, output.Tx, int) ([]output.Capture, error)) ([]output.Capture, error) {
	tx, err := coordinator.Transactor.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	return query(ctx, tx, coordinator.batch())
}

// write runs one short transaction and commits it. A commit error is
// ambiguous -- the transaction may have committed -- and is returned as such;
// the next pass reads what is durably there.
func (coordinator *Coordinator) write(body func(Transaction) error) error {
	tx, err := coordinator.Transactor.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := body(tx); err != nil {
		return err
	}

	return tx.Commit()
}

func (coordinator *Coordinator) String() string {
	return fmt.Sprintf("hangar output capture coordinator, batches of %d", coordinator.batch())
}
