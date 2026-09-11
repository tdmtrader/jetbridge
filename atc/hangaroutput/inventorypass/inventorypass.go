// Package inventorypass is the inventory controller's bounded unit of work.
//
// It lives here rather than in cmd/hangar-output-inventory for one reason: the
// composition is where record-precedes-effect, the cursor/debt commit and the
// absence reconciliation actually are as executable behaviour, and 900 lines of
// `package main` is 900 lines nothing can drive. The binary is left holding flag
// parsing and role construction, which is what a main should be.
//
// It names exactly one output role package -- hangar/output/inventory -- so a
// binary that links it holds one cloud identity's worth of permission.
package inventorypass

import (
	"context"
	"errors"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
)

// DefaultAuditBatch is how many registered generations one pass re-stats.
//
// It is small on purpose. The audit makes one external call per candidate,
// outside every transaction, and a pass that audited the whole deployment would
// hold the inventory lease for the length of the slowest stat in it.
const DefaultAuditBatch = 10

// Pass is one bounded inventory pass.
//
// The order is the whole content of Req 44. The cursor is read under this
// controller's lease fence; one page is listed; every object in it gets a
// disposition -- classified, or recorded as debt; and the debt and the advance
// are committed TOGETHER, so a crash replays the page rather than losing the
// record that an object was never dispositioned at all.
//
// Adoption is attempted per orphan and its refusals are not failures. An
// unresolved reservation, a capture still inside its deadline, a source still
// held: each of those is a "not yet" about one object, and a pass that gave up
// on the page because of one would let a single live capture stop the sweep.
//
// Then the reconciliation, which asks the opposite question: of the generations
// this plane says exist, which ones does the store not have. A listing cannot
// answer that -- nothing in a page reports an object that is not in it -- and
// unexpected exact absence is one of Req 52's three at-risk triggers.
type Pass struct {
	Namespace  output.OutputNamespace
	Inventory  *inventory.Inventory
	Repository *db.HangarOutputRepository
	Transactor controller.Transactor
	Grace      time.Duration

	// AuditBatch bounds the reconciliation. Zero means DefaultAuditBatch.
	AuditBatch int
}

var _ controller.Pass = (*Pass)(nil)

func (pass *Pass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	cursor, err := pass.loadCursor(ctx, lease)
	if err != nil {
		return 0, err
	}

	page, err := pass.Inventory.ListPage(ctx, cursor, output.DefaultPageBudget())
	if err != nil {
		// A listing that did not answer stops the pass WITHOUT advancing. A
		// short list is not the end of a bucket, and a cursor moved by one
		// would make absence authoritative.
		return 0, err
	}

	adopted := 0
	for _, object := range page.Objects {
		taken, err := pass.adopt(ctx, object)
		if err != nil {
			// Recorded against the object and not fatal to the page: one
			// object's refusal must not strand the keys behind it.
			page.Debt = append(page.Debt, output.InventoryDebt{
				ProtocolVersion: output.ProtocolVersion,
				ActivationEpoch: pass.Namespace.ActivationEpoch(),
				ObjectKey:       object.ObjectKey,
				Generation:      object.Generation,
				Reason:          output.DebtStatFailure,
				Attempts:        1,
				ObservedAt:      output.NewTimestamp(time.Now().UTC()),
				Detail:          err.Error(),
			})

			continue
		}
		if taken {
			adopted++
		}
	}

	if err := pass.commit(ctx, page); err != nil {
		return adopted, err
	}

	reconciled, err := pass.Reconcile(ctx)

	return adopted + len(page.Debt) + reconciled, err
}

// loadCursor reads the durable cursor under this controller's fence, and is
// Req 44's corrupt-cursor branch.
//
// A cursor that does not validate is a fact about the cursor and never
// authoritative absence. Propagating the error instead -- which is what this
// did until the reachability guard found RecoverCursor had no production
// caller -- stalls the sweep on that cursor for the life of the deployment,
// which is exactly the "strand later objects" outcome Req 44 exists to prevent.
func (pass *Pass) loadCursor(ctx context.Context, lease output.OperationLease) (output.InventoryCursor, error) {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return output.InventoryCursor{}, err
	}
	defer func() { _ = tx.Rollback() }()

	cursor, err := pass.Repository.LoadInventoryCursor(ctx, tx,
		pass.Namespace.Bucket(), int64(lease.ActivationEpoch), int64(lease.LeaseFence))
	if errors.Is(err, output.ErrCorrupt) {
		// The corrupt value itself is what recovery needs: the position the
		// cursor CLAIMED is the only object identity it has.
		restarted, debt, recoverErr := pass.Inventory.RecoverCursor(cursor)
		if recoverErr != nil {
			return output.InventoryCursor{}, recoverErr
		}
		if err := pass.Repository.AdvanceInventoryCursor(ctx, tx, pass.Namespace.Bucket(),
			restarted, []output.InventoryDebt{debt}); err != nil {
			return output.InventoryCursor{}, err
		}
		if err := tx.Commit(); err != nil {
			return output.InventoryCursor{}, err
		}

		return restarted, nil
	}
	if err != nil {
		return output.InventoryCursor{}, err
	}
	if err := tx.Commit(); err != nil {
		return output.InventoryCursor{}, err
	}

	return cursor, nil
}

// adopt offers one classified object for adoption.
//
// It reports whether a lifecycle row was written. Every refusal the repository
// can give is a fact about one object rather than an error in the pass, which
// is why only an unexpected class comes back as one.
func (pass *Pass) adopt(ctx context.Context, object output.InventoryObject) (bool, error) {
	if !object.Managed {
		// Unmanaged. Recorded by the listing, never relabelled, adopted or
		// deleted.
		return false, nil
	}

	ref := hangar.TreeRef{
		Scope:      object.Marker.Scope,
		Digest:     object.Marker.Digest,
		Generation: object.Generation,
	}

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	outcome, err := pass.Repository.AdoptManagedOrphan(ctx, tx, output.AdoptionRequest{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: pass.Namespace.ActivationEpoch(),
		Ref:             ref,
		Metageneration:  object.Metageneration,
		Marker:          object.Marker,
		CreatedAt:       object.CreatedAt,
		Grace:           pass.Grace,
		SafetyMargin:    output.PublicationGraceMargin,
	})
	if !outcome.Adopted() {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}

	return true, nil
}

// commit writes the page's debt and the cursor advance in ONE transaction.
func (pass *Pass) commit(ctx context.Context, page output.InventoryPage) error {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.AdvanceInventoryCursor(ctx, tx,
		pass.Namespace.Bucket(), page.Next, page.Debt); err != nil {
		return err
	}

	return tx.Commit()
}

// Reconcile stats a bounded batch of the generations this plane believes exist
// and reports the ones the store does not have.
//
// Absence here is NEVER a reclamation. The two are kept apart in the repository
// for the reason rewriting one as the other is dangerous, and the caller's job
// is only to bring the observation: a stat that says 404 for a generation with
// no admitted delete behind it is an out-of-band lifetime violation, and
// RecordOutOfBandAbsence is what refuses to call it anything else.
//
// A stat that fails for any other reason is not absence. Unauthorized, timed
// out and unavailable all mean the store did not answer, and a plane that read
// "we could not ask" as "it is gone" would manufacture violations out of an
// outage.
func (pass *Pass) Reconcile(ctx context.Context) (int, error) {
	refs, err := pass.auditCandidates(ctx)
	if err != nil {
		return 0, err
	}

	reconciled := 0
	var firstErr error
	for _, ref := range refs {
		_, statErr := pass.Inventory.StatExactObject(ctx, ref)
		switch {
		case statErr == nil:
			if err := pass.stamp(ctx, ref); err != nil && firstErr == nil {
				firstErr = err
			}

		case errors.Is(statErr, output.ErrNotFound):
			if err := pass.recordAbsence(ctx, ref); err != nil {
				if firstErr == nil {
					firstErr = err
				}

				continue
			}
			reconciled++

		default:
			// The store did not answer. Nothing is stamped, so the same
			// generation is the first candidate next pass.
			if firstErr == nil {
				firstErr = statErr
			}
		}
	}

	return reconciled, firstErr
}

func (pass *Pass) auditCandidates(ctx context.Context) ([]hangar.TreeRef, error) {
	batch := pass.AuditBatch
	if batch <= 0 {
		batch = DefaultAuditBatch
	}

	tx, err := pass.Transactor.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	refs, err := pass.Repository.LifetimeAuditCandidates(ctx, tx,
		int64(pass.Namespace.ActivationEpoch()), batch)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	return refs, nil
}

func (pass *Pass) stamp(ctx context.Context, ref hangar.TreeRef) error {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.RecordLifetimePresence(ctx, tx, ref); err != nil {
		return err
	}

	return tx.Commit()
}

func (pass *Pass) recordAbsence(ctx context.Context, ref hangar.TreeRef) error {
	tx, err := pass.Transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.Repository.RecordOutOfBandAbsence(ctx, tx, ref); err != nil {
		return err
	}

	return tx.Commit()
}
