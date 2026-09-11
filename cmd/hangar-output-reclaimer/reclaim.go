package main

import (
	"context"
	"errors"
	"flag"
	"time"

	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput/controller"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/concourse/concourse/hangar/output/reclaimer"
)

type controllerConfig struct {
	DSN             string
	Endpoint        string
	Store           string
	Bucket          string
	Prefix          string
	Tenant          string
	ActivationEpoch int64
	Interval        time.Duration
	DeleteTimeout   time.Duration
	Batch           int
}

func (config *controllerConfig) bind(flags *flag.FlagSet) {
	flags.StringVar(&config.DSN, "database", "",
		"PostgreSQL connection string. The controller reads and writes the output plane's tables; it never migrates them.")
	flags.StringVar(&config.Endpoint, "output-endpoint", "",
		"Object-store endpoint. Empty means real GCS with ambient credentials.")
	flags.StringVar(&config.Store, "output-store", output.StoreGCS,
		"Object-store profile. Only the strict native GCS profile is admitted.")
	flags.StringVar(&config.Bucket, "output-bucket", "",
		"The dedicated output bucket. It is never the durable cache bucket or the strict-input bucket.")
	flags.StringVar(&config.Prefix, "output-prefix", "",
		"Deployment prefix. Derived namespaces hang off it; no caller may choose one.")
	flags.StringVar(&config.Tenant, "output-tenant", "",
		"Opaque tenant identity the scope is derived from.")
	flags.Int64Var(&config.ActivationEpoch, "activation-epoch", 0,
		"The activation epoch this controller speaks for.")
	flags.DurationVar(&config.Interval, "interval", output.WorkerFallbackInterval,
		"Periodic wake, bounded at one minute.")
	flags.DurationVar(&config.DeleteTimeout, "delete-timeout", 2*time.Minute,
		"How long one conditional delete may take. The lease term is derived from it, and work begins only with the timeout plus two minutes of lease remaining.")
	flags.IntVar(&config.Batch, "batch", 10,
		"How many admitted jobs one pass may advance. A pass that drained its whole backlog would hold every other operation behind its slowest item.")
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

// reclaimPass advances each admitted job by one bounded step.
//
// One step, not one job to completion. A job whose store will not answer holds
// up this one generation and nothing else, which is the same rule the capture
// recoverer follows for the same reason.
type reclaimPass struct {
	reclaimer     *reclaimer.Reclaimer
	repository    *db.HangarOutputRepository
	transactor    controller.Transactor
	deleteTimeout time.Duration
	batch         int
}

func (pass *reclaimPass) Run(ctx context.Context, lease output.OperationLease) (int, error) {
	jobs, err := pass.due(ctx)
	if err != nil {
		return 0, err
	}

	advanced := 0
	for _, job := range jobs {
		if err := pass.advance(ctx, job); err != nil {
			// Recorded and skipped. One unreachable store must not stop every
			// other generation in the deployment from being collected.
			continue
		}
		advanced++
	}

	return advanced, nil
}

func (pass *reclaimPass) due(ctx context.Context) ([]db.HangarReclaimJob, error) {
	tx, err := pass.transactor.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	return pass.repository.DueReclaimJobs(ctx, tx, pass.batch)
}

// advance takes one job through exactly one external call.
//
// The admitted-delete record is COMMITTED first. Everything after it is the
// answer to a question this system has already durably said it asked.
func (pass *reclaimPass) advance(ctx context.Context, job db.HangarReclaimJob) error {
	attempt, err := pass.admitDelete(ctx, job)
	if err != nil {
		return err
	}

	// Outside every database lock and every transaction. A store that does not
	// answer holds up this one generation.
	outcome, deleteErr := pass.reclaimer.DeleteExactGeneration(ctx, job.Ref,
		output.DeletePrecondition{
			Generation:     job.Ref.Generation,
			Metageneration: job.Metageneration,
		})

	return pass.record(ctx, job, attempt, outcome, deleteErr)
}

func (pass *reclaimPass) admitDelete(ctx context.Context, job db.HangarReclaimJob) (int64, error) {
	tx, err := pass.transactor.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	attempt, err := pass.repository.AdmitDelete(ctx, tx, job, pass.deleteTimeout)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}

	return attempt, nil
}

// record writes the outcome and, where the evidence supports one, the
// finalization.
//
// The mapping from a store's answer to a job's outcome is the whole of Req 49,
// and every arm of it is about what this plane is entitled to CLAIM. A
// confirmed delete finalizes confirmed. Absence finalizes inferred, because
// there IS a prior admitted delete -- that record was committed above. A
// generation conflict is debt and never a broader retry. Anything else leaves
// the job open for the next pass under a renewed lease.
func (pass *reclaimPass) record(ctx context.Context, job db.HangarReclaimJob, attempt int64, outcome output.DeleteOutcome, deleteErr error) error {
	tx, err := pass.transactor.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := pass.repository.RecordDeleteOutcome(ctx, tx, job, attempt, outcome); err != nil {
		return err
	}

	switch {
	case outcome == output.DeleteConfirmed:
		if err := pass.repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimConfirmed, false); err != nil {
			return err
		}

	case outcome == output.DeleteAlreadyAbsent:
		// Absent, with a durable admitted delete behind it. That is INFERRED
		// and never confirmed: this plane did not see the acknowledgement, and
		// claiming it would be claiming evidence it does not have.
		if err := pass.repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimInferred, true); err != nil {
			return err
		}

	case outcome == output.DeleteGenerationConflict:
		if err := pass.repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimConflicted, false); err != nil {
			return err
		}

	case outcome == output.DeleteUnauthorized:
		// The principal lost its grant. The generation goes back to being
		// protected rather than being deleted on a guess, and an operator has
		// an IAM problem to look at.
		if err := pass.repository.FinalizeReclaim(ctx, tx, job,
			output.ReclaimAbandoned, false); err != nil {
			return err
		}

	default:
		// A timeout or an infrastructure failure. The job stays open: the next
		// pass re-admits a delete under the same lease, and the object's
		// absence -- if the first one did land -- is then inferred rather than
		// mistaken for somebody else's deletion.
		if deleteErr != nil && !errors.Is(deleteErr, output.ErrTimeout) &&
			!errors.Is(deleteErr, output.ErrInfrastructure) {
			return deleteErr
		}
	}

	return tx.Commit()
}
