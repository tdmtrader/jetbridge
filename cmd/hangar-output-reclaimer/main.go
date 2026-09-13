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
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"code.cloudfoundry.org/lager/v3/lagerctx"
	"github.com/google/uuid"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/atc/hangaroutput/reclaimpass"
	"github.com/concourse/concourse/hangar/gcsdelete"
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
	if err := output.ValidatePublicationGrace(config.Grace); err != nil {
		return err
	}

	// The capability, from the one package in this repository that can
	// construct it over a real cloud client. Three guards hold this: no other
	// main under cmd/ links that package, the shared objectstore.Handle the
	// other three roots hold has no Delete at all, and no file under cmd/ --
	// this one included -- names cloud.google.com/go/storage, so not even this
	// root can reach the SDK around the capability package.
	objects, closeObjects, err := gcsdelete.NewDeleteClient(ctx, config.Endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = closeObjects() }()

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
	owner := uuid.NewString()
	term := output.LeaseTermFor(config.DeleteTimeout)

	// Two runners, two kinds, two leases. Admission decides; the delete pass
	// acts. A single lease over both would let one unreachable store hold the
	// decision queue behind it, and the schema names them separately for that
	// reason.
	admission := &controller.Runner{
		Kind:            output.OperationReclaimAdmission,
		ActivationEpoch: config.ActivationEpoch,
		OwnerID:         owner,
		Term:            output.MinLeaseTerm,
		Transactor:      transactor,
		Leases:          repository,
		Pass: &reclaimpass.AdmissionPass{
			Repository: repository,
			Transactor: transactor,
			Grace:      config.Grace,
			Term:       term,
			Batch:      config.Batch,
			OwnerID:    owner,
		},
		Reporter: controller.ReporterFunc(logPass),
	}

	deletes := &controller.Runner{
		Kind:            output.OperationReclaimDelete,
		ActivationEpoch: config.ActivationEpoch,
		OwnerID:         owner,
		Term:            term,
		Transactor:      transactor,
		Leases:          repository,
		Pass: &reclaimpass.DeletePass{
			Reclaimer:     sweeper,
			Repository:    repository,
			Transactor:    transactor,
			DeleteTimeout: config.DeleteTimeout,
			Batch:         config.Batch,
			OwnerID:       owner,
			Term:          term,
		},
		Reporter: controller.ReporterFunc(logPass),
	}

	logger := lager.NewLogger("hangar-output-reclaimer")
	logger.RegisterSink(lager.NewWriterSink(os.Stdout, lager.INFO))
	ctx = lagerctx.NewContext(ctx, logger)

	// The acceleration. A reclaim job is admitted by the pass above and worked
	// by the pass below, and between them is a wait: without a notification the
	// delete begins up to one periodic interval after the decision to delete.
	// The fallback is what makes the work always FOUND; this is what makes it
	// found promptly, and a failure here is logged and run past.
	accelerated, closeBus := listenForAdmissions(ctx, config.DSN, conn)
	defer closeBus()

	return loop(ctx, []*controller.Runner{admission, deletes},
		controller.Interval(output.OperationReclaimDelete, config.Interval), accelerated)
}

// listenForAdmissions subscribes to the channel AdmitReclaim notifies.
//
// It returns a nil Acceleration when it cannot subscribe, and that is the whole
// error policy: notification accelerates work and is never the way work is
// found, so a controller that could not listen runs on its periodic wake and
// says so once rather than failing to start.
func listenForAdmissions(ctx context.Context, dsn string, conn *sql.DB) (controller.Acceleration, func()) {
	logger := lagerctx.FromContext(ctx)

	pool, err := controller.OpenListenerPool(ctx, dsn)
	if err != nil {
		logger.Info("hangar-output-acceleration-unavailable", lager.Data{
			"error":  err.Error(),
			"effect": "none on correctness: work is found by the periodic database-clock pass",
		})

		return nil, func() {}
	}

	bus := db.NewNotificationsBus(db.NewPgxListener(pool), conn)
	channel := output.NotifyChannel(output.OperationReclaimDelete)
	signal, err := bus.ListenSignal(channel)
	if err != nil {
		logger.Info("hangar-output-acceleration-unavailable", lager.Data{
			"error":  err.Error(),
			"effect": "none on correctness: work is found by the periodic database-clock pass",
		})
		_ = bus.Close()
		pool.Close()

		return nil, func() {}
	}

	return signal, func() {
		_ = bus.UnlistenSignal(channel, signal)
		_ = bus.Close()
		pool.Close()
	}
}

// loop is the periodic wake, plus whatever accelerates it.
//
// The ticker is unconditional and the acceleration is optional, which is the
// direction Req 50 states: a worker woken only by NOTIFY is silenced by one
// missed notification until it restarts. A wake from either source runs the
// same bounded pass over the same full query, so a coalesced notification and a
// lost one cost the same thing -- nothing.
func loop(ctx context.Context, runners []*controller.Runner, interval time.Duration, accelerated controller.Acceleration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var woken <-chan struct{}
	if accelerated != nil {
		woken = accelerated.C()
	}

	for {
		for _, runner := range runners {
			if err := runner.Run(ctx); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-woken:
		}
	}
}

func logPass(ctx context.Context, kind output.OperationKind, processed int, class string) {
	lagerctx.FromContext(ctx).Info("hangar-output-pass", lager.Data{
		"kind": string(kind), "processed": processed, "class": class,
	})
}
