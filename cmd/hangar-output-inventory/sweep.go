package main

import (
	"context"
	"flag"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
)

// controllerConfig is what an output-plane controller is told.
//
// The bucket, prefix and tenant are configuration and never a request field:
// the namespace is derived from authenticated server configuration plus the
// active epoch, and no caller anywhere in this plane can choose one.
type controllerConfig struct {
	DSN             string
	Endpoint        string
	Store           string
	Bucket          string
	Prefix          string
	Tenant          string
	ActivationEpoch int64
	Interval        time.Duration
	Grace           time.Duration
}

func (config *controllerConfig) bind(flags *flag.FlagSet) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. The controller reads and writes the output plane's tables; it never migrates them, because the activation epoch is what attests compatible migrations.")
	flags.StringVar(&config.Endpoint, "output-endpoint", "",
		"Object-store endpoint. Empty means real GCS with ambient credentials; a value is the emulator profile used by CI and the conformance tier.")
	flags.StringVar(&config.Store, "output-store", output.StoreGCS,
		"Object-store profile. Only the strict native GCS profile is admitted.")
	flags.StringVar(&config.Bucket, "output-bucket", "",
		"The dedicated output bucket. It is never the durable cache bucket or the caller-published strict-input bucket.")
	flags.StringVar(&config.Prefix, "output-prefix", "",
		"Deployment prefix. Derived namespaces hang off it; no caller may choose one.")
	flags.StringVar(&config.Tenant, "output-tenant", "",
		"Opaque tenant identity the scope is derived from.")
	flags.Int64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The activation epoch this controller speaks for. A stale epoch authorizes nothing.")
	flags.DurationVar(&config.Interval, "interval", output.WorkerFallbackInterval,
		"Periodic wake. Bounded at one minute: a worker that woke only on NOTIFY would be silenced by one missed notification until it restarted.")
	flags.DurationVar(&config.Grace, "publication-grace", output.DefaultPublicationGrace,
		"How long a marked, unregistered object is left alone before it may be treated as an orphan. Startup refuses a value that does not exceed the maximum capture deadline by an hour.")
}

func (config controllerConfig) namespace() (output.OutputNamespace, error) {
	return output.DeriveNamespace(output.NamespaceConfig{
		Store:            config.Store,
		Bucket:           config.Bucket,
		DeploymentPrefix: config.Prefix,
		TenantID:         config.Tenant,
		ActivationEpoch:  executioncontrol.ActivationEpoch(config.ActivationEpoch),
	})
}

// sweepPass is one bounded inventory pass.
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
type sweepPass struct {
	namespace  output.OutputNamespace
	inventory  *inventory.Inventory
	repository *db.HangarOutputRepository
	transactor controller.Transactor
	grace      time.Duration
}

func (pass *sweepPass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	cursor, err := pass.loadCursor(ctx, lease)
	if err != nil {
		return 0, err
	}

	page, err := pass.inventory.ListPage(ctx, cursor, output.DefaultPageBudget())
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
				ActivationEpoch: pass.namespace.ActivationEpoch(),
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

	return adopted + len(page.Debt), nil
}

func (pass *sweepPass) loadCursor(ctx context.Context, lease output.OperationLease) (output.InventoryCursor, error) {
	tx, err := pass.transactor.Begin()
	if err != nil {
		return output.InventoryCursor{}, err
	}
	defer func() { _ = tx.Rollback() }()

	cursor, err := pass.repository.LoadInventoryCursor(ctx, tx,
		pass.namespace.Bucket(), int64(lease.ActivationEpoch), int64(lease.LeaseFence))
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
func (pass *sweepPass) adopt(ctx context.Context, object output.InventoryObject) (bool, error) {
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

	tx, err := pass.transactor.Begin()
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	outcome, err := pass.repository.AdoptManagedOrphan(ctx, tx, output.AdoptionRequest{
		ProtocolVersion: output.ProtocolVersion,
		ActivationEpoch: pass.namespace.ActivationEpoch(),
		Ref:             ref,
		Metageneration:  object.Metageneration,
		Marker:          object.Marker,
		CreatedAt:       object.CreatedAt,
		Grace:           pass.grace,
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
func (pass *sweepPass) commit(ctx context.Context, page output.InventoryPage) error {
	tx, err := pass.transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.repository.AdvanceInventoryCursor(ctx, tx,
		pass.namespace.Bucket(), page.Next, page.Debt); err != nil {
		return err
	}

	return tx.Commit()
}
