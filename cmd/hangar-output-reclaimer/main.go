// Command hangar-output-reclaimer is the only process in this system that
// deletes a published object.
//
// GCS IAM cannot require a caller to send a generation precondition once delete
// permission exists, so the requirement lives in the code and in the workload
// boundary together: this binary is the only one that links
// hangar/output/reclaimer, its cloud principal is the only one with
// storage.objects.delete, and the interface it holds has one delete method that
// takes an exact registered ref and an explicit precondition. There is no
// key-only and no unconditional route to fall back to, and an architecture guard
// fails the suite if a second root can reach one.
//
// The order of every reclamation is: admit durably (the generation is marked
// `reclaiming` before anything external happens), record the admitted delete and
// COMMIT it, then ask the store. Ask-then-record has a window in which the
// object is gone and nothing durable says this system asked for it -- and
// absence with no prior admitted delete is an out-of-band lifetime violation
// rather than a reclamation.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	hangargcs "github.com/concourse/concourse/hangar/gcs"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

func main() {
	config := controllerConfig{}
	config.bind(flag.CommandLine)
	flag.Parse()

	if err := run(context.Background(), config); err != nil {
		fmt.Fprintf(os.Stderr, "hangar-output-reclaimer: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, config controllerConfig) error {
	namespace, err := config.namespace()
	if err != nil {
		return err
	}

	client, err := hangargcs.NewStorageClient(ctx, config.Endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	objects, err := hangargcs.NewObjectClient(client)
	if err != nil {
		return err
	}

	// The narrowing. Below this line there is a stat and a conditional delete,
	// and no writer and no list: a reclaimer that could read could exfiltrate,
	// and a reclaimer that could write could resurrect.
	sweeper, err := reclaimer.New(namespace, reclaimer.Restrict(objects))
	if err != nil {
		return err
	}

	conn, err := controller.OpenDatabase(config.DSN, 2)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	repository := db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
	transactor := controller.SQLTransactor{DB: conn, CommitError: db.HangarCommitError}

	runner := &controller.Runner{
		Kind:            output.OperationReclaimDelete,
		ActivationEpoch: config.ActivationEpoch,
		OwnerID:         uuid.NewString(),
		Term:            output.LeaseTermFor(config.DeleteTimeout),
		Transactor:      transactor,
		Leases:          repository,
		Pass: &reclaimPass{
			reclaimer:     sweeper,
			repository:    repository,
			transactor:    transactor,
			deleteTimeout: config.DeleteTimeout,
			batch:         config.Batch,
		},
		Reporter: controller.ReporterFunc(logPass),
	}

	return loop(ctx, runner, controller.Interval(config.Interval))
}

func loop(ctx context.Context, runner *controller.Runner, interval time.Duration) error {
	logger := lager.NewLogger("hangar-output-reclaimer")
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
