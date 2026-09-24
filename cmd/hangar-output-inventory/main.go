// Command hangar-output-inventory sweeps the dedicated output bucket.
//
// It is the only workload in this system whose cloud principal holds bucket-wide
// list, and it is a separate binary for the reason every workload in this plane
// is: a Kubernetes service account is Pod-wide, so a process that links this
// role has this role's permission for everything else it does. It links
// hangar/output/inventory and no other role package, and the store interface it
// is given has no writer and no delete -- those calls do not exist to make.
//
// What it does per wake is bounded by construction: one page, at most 100
// objects, 8 MiB of decoded metadata and 30 seconds, under a lease it holds on
// the database clock. Per-object debt is committed in the same transaction as
// the cursor advance, so a poisoned object cannot be lost and cannot be replayed
// forever.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/concourse/concourse/hangar/diskclient"
	"github.com/concourse/concourse/hangar/objectstore"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/atc/hangaroutput/inventorypass"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/inventory"
)

func main() {
	config := controllerConfig{}
	config.bind(flag.CommandLine)
	flag.Parse()

	if err := run(context.Background(), config); err != nil {
		fmt.Fprintf(os.Stderr, "hangar-output-inventory: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, config controllerConfig) error {
	namespace, err := config.namespace()
	if err != nil {
		return err
	}
	if err := output.ValidatePublicationGrace(config.Grace); err != nil {
		return err
	}

	// The narrowing starts here and there is nothing above it: this root never
	// holds a *storage.Client, so an object handle -- and the delete on it --
	// is not reachable from anything in scope. Everything below holds an
	// interface with List and a stat and nothing else.
	var objects objectstore.Client
	closeObjects := func() error { return nil }
	if config.Store == output.StoreDisk {
		objects, err = diskclient.New(diskclient.Config{Endpoint: config.Endpoint, StoreID: config.StoreID, TokenFile: config.TokenFile, CACert: config.CACert, Timeout: 2 * time.Minute})
	} else {
		objects, closeObjects, err = hangargcs.NewObjectClient(ctx, config.Endpoint)
	}
	if err != nil {
		return err
	}
	defer func() { _ = closeObjects() }()

	sweep, err := inventory.New(namespace, inventory.Restrict(objects),
		output.ClockFunc(func() time.Time { return time.Now().UTC() }))
	if err != nil {
		return err
	}

	conn, err := controller.OpenDatabase(resolveDSN(config.DSN), 2)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	repository := db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
	transactor := controller.SQLTransactor{DB: conn, CommitError: db.HangarCommitError}

	runner := &controller.Runner{
		Kind:            output.OperationInventory,
		ActivationEpoch: config.ActivationEpoch,
		OwnerID:         uuid.NewString(),
		Term:            output.MinLeaseTerm,
		Transactor:      transactor,
		Leases:          repository,
		Pass: &inventorypass.Pass{
			Namespace:  namespace,
			Inventory:  sweep,
			Repository: repository,
			Transactor: transactor,
			Grace:      config.Grace,
		},
		Reporter: controller.ReporterFunc(logPass),
	}

	return loop(ctx, runner, controller.Interval(output.OperationInventory, config.Interval))
}

// loop is the periodic wake. It is nonzero by construction: component.Runner
// with a zero interval wakes only on NOTIFY, and a missed notification would
// strand eligible work until a restart.
func loop(ctx context.Context, runner *controller.Runner, interval time.Duration) error {
	logger := lager.NewLogger("hangar-output-inventory")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))
	ctx = lagerctx.NewContext(ctx, logger)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := runner.Run(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func logPass(ctx context.Context, kind output.OperationKind, processed int, class string) {
	lagerctx.FromContext(ctx).Info("hangar-output-pass", lager.Data{
		"kind": string(kind), "processed": processed, "class": class,
	})
}
