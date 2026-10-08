// Package reclaim is the web's two deleting passes over the output namespace:
// the reclaim pass (an unclaimed generation is deleted and stamped reclaimed)
// and the orphan sweep.
//
// They are web components and not a separate workload because the web is the
// only process that can both read the lifecycle rows and hold the output
// namespace's delete credential; node daemons publish and never delete. Both
// passes take the same PostgreSQL advisory lock, so across every web replica at
// most one of them is deleting at a time, and no reclaim interleaves with an
// orphan verdict.
//
// This package and the durable cache tier are the only places a delete client
// over a real backend is constructed (hangar/architecture_test.go).
package reclaim

import (
	"context"
	"errors"
	"fmt"
	"time"

	"code.cloudfoundry.org/lager/v3/lagerctx"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/db/lock"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/atc/metric"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/disk"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/objectstore"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

// LockID is the one advisory lock both passes run under.
var LockID = lock.NewTaskLockID("hangar_output_deletes")

// StoreConfig is how the web reaches the output namespace's store.
type StoreConfig struct {
	Store    string // output.StoreGCS or output.StoreDisk
	Endpoint string
	StoreID  string
	CACert   string
	Timeout  time.Duration

	// ListTokenFile and DeleteTokenFile are the disk store's two role
	// credentials: list-and-stat for the sweep, stat-and-delete for the
	// delete. GCS uses the web's ambient credential for both.
	ListTokenFile   string
	DeleteTokenFile string
}

// OpenStore constructs the sweep's lister and the reclaimer -- the one delete
// role -- over the output namespace. It is the only place in the web that
// names a delete constructor.
func OpenStore(ctx context.Context, config StoreConfig, namespace output.OutputNamespace) (Lister, *reclaimer.Reclaimer, func() error, error) {
	lister, deleter, closer, err := openClients(ctx, config)
	if err != nil {
		return nil, nil, nil, err
	}
	deletes, err := reclaimer.New(namespace, reclaimer.Restrict(deleter))
	if err != nil {
		_ = closer()
		return nil, nil, nil, err
	}
	return lister, deletes, closer, nil
}

func openClients(ctx context.Context, config StoreConfig) (objectstore.Client, objectstore.DeleteClient, func() error, error) {
	if config.Store == output.StoreDisk {
		lister, err := disk.NewClient(disk.ClientConfig{Endpoint: config.Endpoint, StoreID: config.StoreID,
			TokenFile: config.ListTokenFile, CACert: config.CACert, Timeout: config.Timeout})
		if err != nil {
			return nil, nil, nil, err
		}
		deleter, err := disk.NewDeleteClient(disk.ClientConfig{Endpoint: config.Endpoint, StoreID: config.StoreID,
			TokenFile: config.DeleteTokenFile, CACert: config.CACert, Timeout: config.Timeout})
		if err != nil {
			return nil, nil, nil, err
		}
		return lister, deleter, func() error { return nil }, nil
	}

	lister, closeLister, err := hangargcs.NewClient(ctx, config.Endpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	deleter, closeDeleter, err := hangargcs.NewDeleteClient(ctx, config.Endpoint)
	if err != nil {
		_ = closeLister()
		return nil, nil, nil, err
	}
	return lister, deleter, func() error { return errors.Join(closeLister(), closeDeleter()) }, nil
}

// exclusively runs fn under LockID, or not at all when another web holds it,
// and reports whether it ran. A nil locker runs fn unlocked: the specs drive
// the passes without a lock factory.
func exclusively(ctx context.Context, locker lock.LockFactory, fn func() error) (bool, error) {
	if locker == nil {
		return true, fn()
	}
	logger := lagerctx.FromContext(ctx)
	held, acquired, err := locker.Acquire(logger, LockID)
	if err != nil {
		return false, err
	}
	if !acquired {
		logger.Debug("hangar-output-deletes-held-elsewhere")
		return false, nil
	}
	defer func() { _ = held.Release() }()

	return true, fn()
}

// Pass is the reclaim component: one bounded pass that deletes every
// registered generation nothing holds, and stamps each one reclaimed.
type Pass struct {
	Locker     lock.LockFactory
	Transactor hangaroutput.Transactor
	Repository *db.HangarOutputRepository
	Reclaimer  *reclaimer.Reclaimer

	// Grace is the publication grace; a generation is reclaimable only once
	// it has elapsed since registration, measured on the database clock.
	Grace time.Duration

	// DeleteTimeout bounds one conditional delete.
	DeleteTimeout time.Duration

	// Batch bounds how many generations one pass reclaims. Zero is defaultBatch.
	Batch int
}

const defaultBatch = 10

func (pass *Pass) batch() int {
	if pass.Batch <= 0 {
		return defaultBatch
	}
	return pass.Batch
}

// DefaultDeleteTimeout bounds one conditional delete when none is configured.
const DefaultDeleteTimeout = 2 * time.Minute

func deleteTimeout(configured time.Duration) time.Duration {
	if configured <= 0 {
		return DefaultDeleteTimeout
	}
	return configured
}

// Run is one pass: select what is reclaimable, then reclaim each candidate
// in a transaction of its own.
func (pass *Pass) Run(ctx context.Context) error {
	_, err := exclusively(ctx, pass.Locker, func() error {
		reclaimed, deferred, failed, err := pass.Reclaim(ctx)
		metric.HangarOutputReclaimPass{Reclaimed: reclaimed, Deferred: deferred, Failed: failed}.
			Emit(lagerctx.FromContext(ctx))
		return err
	})
	return err
}

// Reclaim deletes one bounded batch of unclaimed generations. It reports how
// many it reclaimed, how many it deferred to a later pass (protected between
// the query and the lock, or still inside grace), and how many deletes failed
// to answer. It is exported for the acceptance specs; Run is the component.
//
// The candidate query excludes a generation with a live claim, a pending or
// publishing capture of its tree or an unregistered input publication of its
// tree; HoldForReclaim rechecks every one under the tree and lifecycle locks,
// which are held across the delete and the stamp.
func (pass *Pass) Reclaim(ctx context.Context) (int, int, int, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	candidates, err := pass.Repository.ReclaimableGenerations(ctx, tx, pass.Grace, pass.batch())
	_ = tx.Rollback()
	if err != nil {
		return 0, 0, 0, err
	}

	reclaimed, deferred, failed := 0, 0, 0
	var firstErr error
	for _, ref := range candidates {
		err := pass.reclaimOne(ctx, ref)
		switch {
		case err == nil:
			reclaimed++
		case errors.Is(err, output.ErrConflict), errors.Is(err, output.ErrNotFound),
			errors.Is(err, output.ErrAtRisk):
			// Something took a claim or started a capture between the query
			// and the lock, or an integrity finding is open: "not yet".
			deferred++
		default:
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}

	return reclaimed, deferred, failed, firstErr
}

// reclaimOne is the act, in one transaction: hold the generation under the
// tree and lifecycle locks, ask the store to delete its exact generation,
// and stamp it reclaimed once the store answered. The locks are held across
// the network call on purpose: a claimant that arrives meanwhile waits on the
// lifecycle row and then finds the generation reclaimed, which is the one
// outcome a hold taken beside a delete must never produce. A pass that dies
// between the delete and the commit leaves the row registered; the next pass
// retries the delete and gets already-absent.
//
// Delete outcomes collapse to one stamp. Confirmed and already-absent are
// reclaimed. A generation conflict is reclaimed too: the exact generation is
// gone, and the object now at its key belongs to another registration (or to
// nobody, and the orphan sweep judges it by its own marker); the conflict is
// logged, never retried unconditionally. Unauthorized records a principal
// denial, which blocks the plane until an operator resolves it, and the row
// stays registered. A timeout or an infrastructure failure settles nothing:
// the transaction rolls back and the next pass asks again.
func (pass *Pass) reclaimOne(ctx context.Context, ref hangar.TreeRef) error {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.HoldForReclaim(ctx, tx, ref, pass.Grace); err != nil {
		return err
	}

	deleteCtx, cancel := context.WithTimeout(ctx, deleteTimeout(pass.DeleteTimeout))
	outcome, deleteErr := pass.Reclaimer.DeleteExactGeneration(deleteCtx, ref,
		output.DeletePrecondition{Generation: ref.Generation})
	cancel()

	logger := lagerctx.FromContext(ctx)
	switch outcome {
	case reclaimer.Deleted, reclaimer.AlreadyAbsent:
	case reclaimer.GenerationConflict:
		// A count-free line: the ref is an opaque identity the redaction rule
		// keeps out of logs; the stamped row names it for an operator.
		logger.Info("hangar-output-reclaim-generation-conflict")
	case reclaimer.Unauthorized:
		if err := pass.Repository.RecordRuntimePrincipalDenial(ctx, tx, output.PrincipalReclaimer,
			"the object store refused the web a conditional delete its reclaim credential is "+
				"configured to hold"); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		return fmt.Errorf("%w: the object store refused the reclaim pass a conditional delete of "+
			"%s/%s/%d", output.ErrUnauthorized, ref.Scope, ref.Digest, ref.Generation)
	default:
		if deleteErr == nil {
			deleteErr = fmt.Errorf("%w: the delete of %s/%s/%d did not answer", output.ErrInfrastructure,
				ref.Scope, ref.Digest, ref.Generation)
		}
		return deleteErr
	}

	if err := pass.Repository.StampReclaimed(ctx, tx, ref); err != nil {
		return err
	}

	return tx.Commit()
}
