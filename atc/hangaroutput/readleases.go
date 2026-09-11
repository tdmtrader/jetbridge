package hangaroutput

// The read-lease recovery worker.
//
// A read lease is the READER's protection and it outlives the claim: releasing
// the last claim during a transfer must not delete the bytes out from under a
// materializing task. The cost of that is a lease nobody closes when the
// materializer dies mid-transfer, and an active lease refuses reclaim admission
// -- so one crashed reader would pin its generation against collection for the
// life of the deployment.
//
// Expiry on the DATABASE clock is what bounds it, and expiry alone does not
// close anything. Something has to notice, and this is that something. Until
// Phase 7 the repository method existed, was specified, and had no caller at
// all: the review that found it named the gap precisely -- "the lease closes
// only by expiry plus recovery, which has no worker until Phase 7".
//
// It takes no cloud permission, opens no store client and reads no object. What
// it does is ask the database which of its own leases its own clock says have
// expired, and close exactly those.

import (
	"context"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/hangar/output"
)

// DefaultReadLeaseBatch bounds one pass.
//
// It is a batch and not a limit on leases: whatever is left is found on the
// next periodic wake, and a pass that closed a deployment's whole backlog on
// the first wake after an outage would hold the component runner behind it.
const DefaultReadLeaseBatch = 100

// AbandonedReadLeases is the durable half, declared where it is consumed.
type AbandonedReadLeases interface {
	CloseAbandonedReadLeases(ctx context.Context, tx output.Tx, limit int) (int, error)
}

// ReadLeaseCleaner is the component.
type ReadLeaseCleaner struct {
	Transactor Transactor
	Leases     AbandonedReadLeases
	BatchSize  int
}

func (cleaner *ReadLeaseCleaner) batchSize() int {
	if cleaner.BatchSize <= 0 {
		return DefaultReadLeaseBatch
	}

	return cleaner.BatchSize
}

// Run closes one bounded batch of expired leases.
//
// The predicate is the repository's and it is repeated there under the lock, so
// a lease renewed between the candidate read and the write is not closed by a
// decision taken from the stale answer. Nothing about that belongs here: this
// is a wake, a bounded call and a commit.
func (cleaner *ReadLeaseCleaner) Run(ctx context.Context) error {
	tx, err := cleaner.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	closed, err := cleaner.Leases.CloseAbandonedReadLeases(ctx, tx, cleaner.batchSize())
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	if closed > 0 {
		// A COUNT and nothing else. A log line naming the lease would put an
		// opaque id a consumer may treat as sensitive into a system nobody
		// thinks of as a log, and a metric keyed by one would be a series per
		// reader forever.
		lagerctx.FromContext(ctx).Info("hangar-output-read-leases-closed", lager.Data{
			"closed": closed,
		})
	}

	return nil
}

func (cleaner *ReadLeaseCleaner) String() string {
	return "hangar output read-lease cleaner"
}
