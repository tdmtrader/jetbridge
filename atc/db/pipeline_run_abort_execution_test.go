package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// An aborted Run build that could not close its own execution -- no web was
// tracking it when it was aborted, or its in-band stop could not prove an
// outcome -- stays unfinished while the execution is open. Aborting a build
// is scoped to that build: it never asks for its Run's cancellation, so the
// Run keeps running and its other work, and a rerun of the job, go on.
var _ = Describe("Finishing an aborted Run build with an unclosed execution", func() {
	var (
		ctx      context.Context
		factory  db.PipelineRunFactory
		creation db.RunCreation
		build    db.Build
		other    db.Build
		signer   *executioncontrol.AcknowledgementSigner
		verifier hangaroutput.ControlKeyRing
	)

	cancellationRequested := func() (bool, string) {
		var requested bool
		var by string
		Expect(dbConn.QueryRow(`SELECT cancel_requested_at IS NOT NULL, coalesce(cancel_requested_by,'') FROM pipeline_runs WHERE id=$1`,
			creation.Run.ID()).Scan(&requested, &by)).To(Succeed())
		return requested, by
	}

	// admitStartedFor admits a build's task and retains the node's signed
	// start, leaving it with no closure, as a command still unaccounted for.
	admitStartedFor := func(build db.Build) db.RunExecutionAdmission {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		admission, owned, err := factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
			BuildID: build.ID(), PlanID: "task-step", Kind: db.ContainerTypeTask,
			Epoch: 1, NodeName: "node", NodeUID: "node-uid",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeTrue())
		start, err := signer.Sign(executioncontrol.Acknowledgement{
			ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementStart,
			Identity: admission.Identity, ActivationEpoch: 1, LedgerSequence: 1,
			NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: "task-process",
			ObservedAt: output.NewTimestamp(time.Now()),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordRunExecutionWitness(ctx, tx, build.ID(), admission.PlanID, start, verifier)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
		return admission
	}
	admitStarted := func() db.RunExecutionAdmission { return admitStartedFor(build) }

	// discovered claims the cancellation worker's lease and runs one bounded
	// discovery pass, returning every recorded operation as kind -> subjects.
	discovered := func() ([]int, map[db.RunCancellationKind][]string) {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		lease, owned, err := factory.ClaimRunCancellationLease(ctx, tx, "closure-test", time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(owned).To(BeTrue())
		pending, err := factory.PendingRunCancellations(ctx, tx, lease, db.RunCancellationRunLimit)
		Expect(err).NotTo(HaveOccurred())
		if len(pending) > 0 {
			_, err = factory.DiscoverRunCancellation(ctx, tx, lease, creation.Run.ID(), db.RunCancellationOperationLimit)
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(tx.Commit()).To(Succeed())
		rows, err := dbConn.Query(`SELECT kind,subject FROM pipeline_run_cancellation_operations WHERE run_id=$1`, creation.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		defer db.Close(rows)
		operations := map[db.RunCancellationKind][]string{}
		for rows.Next() {
			var kind, subject string
			Expect(rows.Scan(&kind, &subject)).To(Succeed())
			operations[db.RunCancellationKind(kind)] = append(operations[db.RunCancellationKind(kind)], subject)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return pending, operations
	}

	closures := func() []int {
		rows, err := dbConn.Query(`SELECT build_id FROM pipeline_run_build_closures WHERE run_id=$1 ORDER BY build_id`, creation.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		defer db.Close(rows)
		var ids []int
		for rows.Next() {
			var id int
			Expect(rows.Scan(&id)).To(Succeed())
			ids = append(ids, id)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return ids
	}


	// finishExecution retains the node's signed finish for an admitted
	// execution: the node's own report that the process is gone.
	finishExecution := func(b db.Build, admission db.RunExecutionAdmission) {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		finish, err := signer.Sign(executioncontrol.Acknowledgement{
			ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementFinish,
			Identity: admission.Identity, ActivationEpoch: 1, LedgerSequence: 2,
			NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: "task-process",
			ObservedAt: output.NewTimestamp(time.Now()), Outcome: &executioncontrol.ExitOutcome{ExitCode: 143},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordRunExecutionWitness(ctx, tx, b.ID(), admission.PlanID, finish, verifier)).To(Succeed())
		Expect(tx.Commit()).To(Succeed())
	}

	inTx := func(fn func(db.Tx) error) error {
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit()
	}

	// closurePass is one cancellation-worker pass with the node replaced by
	// what the database already knows: an execution is closed only once its
	// finish is retained. Held kinds answer pending, as a slow node would.
	// Backed-off operations are made due first, so each pass sees everything.
	closurePass := func(owner string, held ...db.RunCancellationKind) {
		_, err := dbConn.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=now() - interval '1 second' WHERE completed_at IS NULL`)
		Expect(err).NotTo(HaveOccurred())
		var lease db.RunCancellationLease
		Expect(inTx(func(tx db.Tx) error {
			var owned bool
			lease, owned, err = factory.ClaimRunCancellationLease(ctx, tx, owner, time.Minute)
			Expect(owned).To(BeTrue())
			return err
		})).To(Succeed())
		execute := func(op db.RunCancellationOperation) db.RunCancellationDebt {
			for _, kind := range held {
				if op.Kind == kind {
					return db.CancellationPending
				}
			}
			switch op.Kind {
			case db.CancelExecution:
				var in db.RunCancellationExecution
				if err := inTx(func(tx db.Tx) error {
					var err error
					in, err = factory.CancellationRunExecution(ctx, tx, lease, op)
					return err
				}); err != nil {
					return db.CancellationUnavailable
				}
				if in.Closed {
					return db.CancellationDone
				}
				return db.CancellationPending
			case db.CancelSchedulerDebt:
				debt, _ := factory.ExecuteCancellationOperation(ctx, lease, op)
				return debt
			case db.CancelBuild, db.CancelCandidate, db.CancelTerminalize:
				debt, _ := factory.ExecuteCancellationFinality(ctx, lease, op)
				return debt
			default:
				return db.CancellationUnavailable
			}
		}
		for visit := 0; visit < db.RunCancellationOperationLimit; visit++ {
			var pending []int
			Expect(inTx(func(tx db.Tx) error {
				var err error
				pending, err = factory.PendingRunCancellations(ctx, tx, lease, 1)
				return err
			})).To(Succeed())
			if len(pending) == 0 {
				return
			}
			var op db.RunCancellationOperation
			var found bool
			Expect(inTx(func(tx db.Tx) error {
				if _, err := factory.DiscoverRunCancellation(ctx, tx, lease, pending[0], db.RunCancellationOperationLimit); err != nil {
					return err
				}
				var err error
				op, found, err = factory.ClaimRunCancellationOperation(ctx, tx, lease, pending[0])
				return err
			})).To(Succeed())
			if !found {
				return
			}
			debt := execute(op)
			Expect(inTx(func(tx db.Tx) error { return factory.RecordRunCancellationProgress(ctx, tx, lease, op, debt) })).To(Succeed())
		}
	}

	closureClosed := func() bool {
		var closed bool
		Expect(dbConn.QueryRow(`SELECT closed_at IS NOT NULL FROM pipeline_run_build_closures WHERE build_id=$1`, build.ID()).Scan(&closed)).To(Succeed())
		return closed
	}

	buildState := func(b db.Build) (bool, string) {
		var completed bool
		var status string
		Expect(dbConn.QueryRow(`SELECT completed,status FROM builds WHERE id=$1`, b.ID()).Scan(&completed, &status)).To(Succeed())
		return completed, status
	}

	finalize := func() bool {
		var ready bool
		Expect(inTx(func(tx db.Tx) error {
			var err error
			ready, err = factory.FinalizeOutputRun(ctx, tx, creation.Run.ID())
			return err
		})).To(Succeed())
		return ready
	}

	// schedulerCaughtUp stands in for the scheduler, which no db test runs:
	// the payload's jobs have no schedule request left to serve.
	schedulerCaughtUp := func() {
		_, err := dbConn.Exec(`UPDATE jobs SET last_scheduled=schedule_requested WHERE pipeline_id=(SELECT id FROM pipelines WHERE pipeline_run_id=$1)`, creation.Run.ID())
		Expect(err).NotTo(HaveOccurred())
	}

	runStatus := func() string {
		var status string
		Expect(dbConn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, creation.Run.ID()).Scan(&status)).To(Succeed())
		return status
	}

	// abortOverOpenExecution leaves the aborted build with an execution only
	// the node can close, so finishing it records its build closure.
	abortOverOpenExecution := func() db.RunExecutionAdmission {
		execution := admitStarted()
		Expect(build.MarkAsAborted()).To(Succeed())
		Expect(build.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))
		Expect(closures()).To(Equal([]int{build.ID()}))
		return execution
	}

	BeforeEach(func() {
		ctx = context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("abort-execution-test")
		Expect(err).NotTo(HaveOccurred())
		hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())

		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "aborted-executions"}, atc.Config{
			Template: true,
			Jobs: atc.JobConfigs{
				{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
					Name: "task", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}},
				}}}},
				{Name: "sibling", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
					Name: "task", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}},
				}}}},
			},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarEpoch: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Expect(creation.EntryBuilds).To(HaveLen(2))
		build, other = creation.EntryBuilds[0], creation.EntryBuilds[1]

		public, private, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		signer, err = executioncontrol.NewAcknowledgementSigner(private)
		Expect(err).NotTo(HaveOccurred())
		verifier = hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}}
	})

	It("stays unfinished and leaves its Run running and uncancelled", func() {
		admitStarted()
		Expect(build.MarkAsAborted()).To(Succeed())
		Expect(build.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))

		requested, by := cancellationRequested()
		Expect(requested).To(BeFalse(), "aborting one build cancelled its whole Run (requested by %q)", by)
		var status string
		Expect(dbConn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, creation.Run.ID()).Scan(&status)).To(Succeed())
		Expect(status).To(Equal(string(atc.RunStatusRunning)))

		var completed bool
		Expect(dbConn.QueryRow(`SELECT completed FROM builds WHERE id=$1`, build.ID()).Scan(&completed)).To(Succeed())
		Expect(completed).To(BeFalse(), "the build finished over an unclosed execution")
	})

	It("records one build closure, scoped to that build, and no Run cancellation", func() {
		execution := admitStarted()
		sibling := admitStartedFor(other)
		Expect(build.MarkAsAborted()).To(Succeed())
		Expect(build.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))
		Expect(build.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))
		Expect(closures()).To(Equal([]int{build.ID()}), "one closure, recorded once, for the aborted build only")

		requested, _ := cancellationRequested()
		Expect(requested).To(BeFalse())

		pending, operations := discovered()
		Expect(pending).To(ContainElement(creation.Run.ID()), "the worker never sees a Run whose only open work is a build closure")
		Expect(operations).To(HaveKeyWithValue(db.CancelExecution, ConsistOf(fmt.Sprintf("%s/%d", execution.Identity.ExecutionID, execution.Identity.Fence))))
		Expect(operations).To(HaveKeyWithValue(db.CancelBuild, ConsistOf(strconv.Itoa(build.ID()))))
		for _, kind := range []db.RunCancellationKind{db.CancelSchedulerDebt, db.CancelCandidate, db.CancelTerminalize} {
			Expect(operations).NotTo(HaveKey(kind), "a build closure discovered %s work", kind)
		}
		siblingExecution := fmt.Sprintf("%s/%d", sibling.Identity.ExecutionID, sibling.Identity.Fence)
		for kind, subjects := range operations {
			Expect(subjects).NotTo(ContainElement(siblingExecution), "%s reached another build's execution", kind)
			Expect(subjects).NotTo(ContainElement(strconv.Itoa(other.ID())), "%s reached another build", kind)
		}
	})

	It("closes the closure only once its last operation completes, and only then lets the Run finish", func() {
		Expect(other.Finish(db.BuildStatusSucceeded)).To(Succeed())
		execution := abortOverOpenExecution()

		closurePass("worker")
		Expect(closureClosed()).To(BeFalse(), "the closure closed while the node still held the execution")
		completed, _ := buildState(build)
		Expect(completed).To(BeFalse())

		finishExecution(build, execution)
		closurePass("worker", db.CancelExecution)
		completed, status := buildState(build)
		Expect(completed).To(BeTrue(), "the build did not finish once its execution was closed")
		Expect(status).To(Equal(string(db.BuildStatusAborted)))
		Expect(closureClosed()).To(BeFalse(), "the closure closed before its execution operation completed")
		Expect(finalize()).To(BeFalse(), "the Run was published while its build closure was open")
		Expect(runStatus()).To(Equal(string(atc.RunStatusRunning)))

		closurePass("worker")
		Expect(closureClosed()).To(BeTrue(), "the closure stayed open after its last operation completed")
		schedulerCaughtUp()
		Expect(finalize()).To(BeTrue())
		Expect(runStatus()).To(Equal(string(atc.RunStatusAborted)), "an effective aborted build makes the Run aborted (M-3)")
		requested, _ := cancellationRequested()
		Expect(requested).To(BeFalse(), "the Run was aborted by cancellation, not by ordinary completion")
	})

	It("hands its completed operations over to a later Run cancellation without repeating them", func() {
		execution := abortOverOpenExecution()
		finishExecution(build, execution)
		closurePass("worker", db.CancelBuild)
		var doneAt time.Time
		var attempts int
		Expect(dbConn.QueryRow(`SELECT completed_at,attempt_count FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2`,
			creation.Run.ID(), string(db.CancelExecution)).Scan(&doneAt, &attempts)).To(Succeed())

		Expect(inTx(func(tx db.Tx) error {
			_, err := factory.AcceptRunCancellation(ctx, tx, creation.Run.ID(), "operator", nil)
			return err
		})).To(Succeed())
		for pass := 0; pass < 4 && runStatus() == string(atc.RunStatusRunning); pass++ {
			closurePass("worker")
		}
		Expect(runStatus()).To(Equal(string(atc.RunStatusAborted)))
		var laterAt time.Time
		var laterAttempts int
		Expect(dbConn.QueryRow(`SELECT completed_at,attempt_count FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND kind=$2 AND subject=$3`,
			creation.Run.ID(), string(db.CancelExecution), fmt.Sprintf("%s/%d", execution.Identity.ExecutionID, execution.Identity.Fence)).Scan(&laterAt, &laterAttempts)).To(Succeed())
		Expect(laterAt).To(BeTemporally("==", doneAt), "Run cancellation repeated an operation the closure had completed")
		Expect(laterAttempts).To(Equal(attempts))
		Expect(closureClosed()).To(BeTrue())
	})

	It("refuses closure progress from an expired worker epoch", func() {
		abortOverOpenExecution()
		var stale db.RunCancellationLease
		var op db.RunCancellationOperation
		Expect(inTx(func(tx db.Tx) error {
			var err error
			stale, _, err = factory.ClaimRunCancellationLease(ctx, tx, "first", time.Minute)
			if err != nil {
				return err
			}
			if _, err = factory.DiscoverRunCancellation(ctx, tx, stale, creation.Run.ID(), db.RunCancellationOperationLimit); err != nil {
				return err
			}
			var found bool
			op, found, err = factory.ClaimRunCancellationOperation(ctx, tx, stale, creation.Run.ID())
			Expect(found).To(BeTrue())
			return err
		})).To(Succeed())
		_, err := dbConn.Exec(`UPDATE pipeline_run_cancellation_worker SET renewed_at=now() - interval '2 minutes', expires_at=now() - interval '1 second'`)
		Expect(err).NotTo(HaveOccurred())
		Expect(inTx(func(tx db.Tx) error {
			_, owned, err := factory.ClaimRunCancellationLease(ctx, tx, "second", time.Minute)
			Expect(owned).To(BeTrue())
			return err
		})).To(Succeed())

		err = inTx(func(tx db.Tx) error { return factory.RecordRunCancellationProgress(ctx, tx, stale, op, db.CancellationDone) })
		Expect(err).To(HaveOccurred(), "an expired worker recorded closure progress")
		var completed int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_cancellation_operations WHERE run_id=$1 AND completed_at IS NOT NULL`, creation.Run.ID()).Scan(&completed)).To(Succeed())
		Expect(completed).To(BeZero())
		Expect(closureClosed()).To(BeFalse())
	})

	It("closes, completes and reclaims a Run whose Hangar epoch was disabled by a rotation", func() {
		Expect(other.Finish(db.BuildStatusSucceeded)).To(Succeed())
		execution := abortOverOpenExecution()
		finishExecution(build, execution)
		_, err := dbConn.Exec(`UPDATE hangar_output_activation_epochs SET output_state='disabled', base_state='disabled', revision=revision+1 WHERE epoch_id=1`)
		Expect(err).NotTo(HaveOccurred())

		closurePass("worker")
		closurePass("worker")
		Expect(closureClosed()).To(BeTrue(), "a Hangar epoch rotation stranded the build closure")
		schedulerCaughtUp()
		Expect(finalize()).To(BeTrue(), "a Hangar epoch rotation stranded ordinary completion")
		Expect(runStatus()).To(Equal(string(atc.RunStatusAborted)))

		_, err = dbConn.Exec(`UPDATE pipelines SET run_retention_ttl_days=1 WHERE id=(SELECT template_pipeline_id FROM pipeline_runs WHERE id=$1)`, creation.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		// Age the published Run past its TTL. Its terminal publication is
		// immutable by trigger, so the fixture steps around it the way a
		// clock would, and only for this one column.
		Expect(inTx(func(tx db.Tx) error {
			if _, err := tx.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
				return err
			}
			_, err := tx.Exec(`UPDATE pipeline_runs SET completed_at=completed_at - interval '2 days' WHERE id=$1`, creation.Run.ID())
			return err
		})).To(Succeed())
		destroyed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(creation.Run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue(), "a Hangar epoch rotation stranded reclamation")
	})

	It("records no closure and nothing for the worker when no build was aborted", func() {
		admitStarted()
		Expect(build.Finish(db.BuildStatusFailed)).To(MatchError(atc.ErrRunOutputPending))
		Expect(closures()).To(BeEmpty())
		pending, operations := discovered()
		Expect(pending).NotTo(ContainElement(creation.Run.ID()))
		Expect(operations).To(BeEmpty())
	})

	It("does not cancel the Run for a build that was not aborted", func() {
		admitStarted()
		Expect(build.Finish(db.BuildStatusFailed)).To(MatchError(atc.ErrRunOutputPending))
		requested, _ := cancellationRequested()
		Expect(requested).To(BeFalse())
	})

	It("does not cancel the Run for an aborted build with nothing left open", func() {
		Expect(build.MarkAsAborted()).To(Succeed())
		Expect(build.Finish(db.BuildStatusAborted)).To(Succeed())
		requested, _ := cancellationRequested()
		Expect(requested).To(BeFalse())
	})
})
