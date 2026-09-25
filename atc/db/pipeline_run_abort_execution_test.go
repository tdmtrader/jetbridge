package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
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

		err = inTx(func(tx db.Tx) error {
			return factory.RecordRunCancellationProgress(ctx, tx, stale, op, db.CancellationDone)
		})
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

// An aborted Run build whose output handoff is unsettled -- its producer never
// started, or is still executing -- is closed by its build closure through the
// source operations Run cancellation uses, for that build's own handoff only.
// The node is closureNode, which answers as the exact node would.
var _ = Describe("Closing an aborted Run build's unsettled output handoff", func() {
	var f *closureHandoffs

	BeforeEach(func() {
		f = newClosureHandoffs()
	})

	It("discovers only the aborted build's handoff, hold, capture and execution", func() {
		sibling := f.hold(f.sibling, f.siblingPlan)
		review := f.hold(f.review, f.reviewPlan)
		f.node.start()
		reservation := f.reserveCapture(review)
		f.abortReview()

		operations := f.discover()
		Expect(operations).To(HaveKeyWithValue(db.CancelHandoff, ConsistOf(string(review.HandoffID))))
		Expect(operations).To(HaveKeyWithValue(db.CancelSourceHold, ConsistOf(string(review.SourceHoldID))))
		Expect(operations).To(HaveKeyWithValue(db.CancelCapture, ConsistOf(string(reservation))))
		Expect(operations).To(HaveKeyWithValue(db.CancelExecution, ConsistOf(executionSubject(review.Execution))))
		Expect(operations).To(HaveKeyWithValue(db.CancelBuild, ConsistOf(strconv.Itoa(f.review.ID()))))
		for kind, subjects := range operations {
			Expect(subjects).NotTo(ContainElements(string(sibling.HandoffID), string(sibling.SourceHoldID), executionSubject(sibling.Execution)),
				"%s reached another build's live handoff", kind)
		}

		// The source handler accepts each of them without Run cancellation.
		lease := f.lease("worker")
		for _, kind := range []db.RunCancellationKind{db.CancelHandoff, db.CancelSourceHold, db.CancelCapture, db.CancelExecution} {
			op := f.claimed(lease, kind, operations[kind][0])
			Expect(f.inTx(func(tx db.Tx) error {
				_, err := f.factory.CancellationOutputTask(f.ctx, tx, lease, op)
				return err
			})).To(Succeed(), "the build closure's %s operation was refused", kind)
		}
		Expect(f.cancellationRequested()).To(BeFalse())
	})

	It("closes a never-started producer's handoff, and only then lets the Run complete aborted with every unselected claim released", func() {
		f.publishSibling()
		review := f.hold(f.review, f.reviewPlan)
		f.abortReview()

		f.pass(db.CancelBuild)
		f.pass(db.CancelBuild)
		Expect(f.classification(review)).To(Equal("never_started"))
		Expect(f.evidence(review)).To(Equal("never_started"))
		Expect(f.released(review)).To(BeTrue(), "the closure did not release the aborted build's hold")
		Expect(f.closureClosed()).To(BeFalse(), "the closure closed before its build finished")
		Expect(f.finalize()).To(BeFalse(), "the Run was published while its build closure was open")

		f.pass()
		completed, status := f.buildState(f.review)
		Expect(completed).To(BeTrue())
		Expect(status).To(Equal(string(db.BuildStatusAborted)))
		Expect(f.closureClosed()).To(BeTrue(), "the closure stayed open after its last operation completed")
		Expect(f.closedAfterEveryOperation()).To(BeTrue())

		f.schedulerCaughtUp()
		Expect(f.finalize()).To(BeTrue())
		Expect(f.runStatus()).To(Equal(string(atc.RunStatusAborted)), "an effective aborted build makes the Run aborted (M-3)")
		Expect(f.cancellationRequested()).To(BeFalse())
		Expect(f.activeClaims()).To(BeZero(), "an aborted Run kept an unselected candidate claim")
	})

	It("interrupts an executing producer and releases its hold only on the node's exact finish", func() {
		review := f.hold(f.review, f.reviewPlan)
		f.node.start()
		f.abortReview()

		for pass := 0; pass < 3; pass++ {
			f.pass()
		}
		Expect(f.classification(review)).To(Equal("executing"))
		Expect(f.node.interrupted).To(BeNumerically(">", 0), "the executing producer was never interrupted")
		Expect(f.evidence(review)).To(BeEmpty(), "an interruption request was taken for the producer's outcome")
		Expect(f.released(review)).To(BeFalse(), "the hold was released while the producer was still executing")
		completed, _ := f.buildState(f.review)
		Expect(completed).To(BeFalse())
		Expect(f.closureClosed()).To(BeFalse())

		f.node.finish(f, review, 143)
		for pass := 0; pass < 3 && !f.closureClosed(); pass++ {
			f.pass()
		}
		Expect(f.evidence(review)).To(Equal("authoritative_finish"))
		Expect(f.released(review)).To(BeTrue())
		Expect(f.releasedAfterEvidence(review)).To(BeTrue(), "the hold was released before the node's finish was retained")
		Expect(f.closureClosed()).To(BeTrue())
		Expect(f.runStatus()).To(Equal(string(atc.RunStatusRunning)))
	})

	It("refuses source work on another build's handoff without Run cancellation", func() {
		sibling := f.hold(f.sibling, f.siblingPlan)
		f.hold(f.review, f.reviewPlan)
		f.abortReview()
		f.discover()

		lease := f.lease("worker")
		op := f.claimed(lease, db.CancelHandoff, string(sibling.HandoffID))
		err := f.inTx(func(tx db.Tx) error {
			_, err := f.factory.CancellationOutputTask(f.ctx, tx, lease, op)
			return err
		})
		Expect(err).To(MatchError(db.ErrRunCancellationProgressStale), "a build closure reached another build's handoff")
	})

	It("yields its handoff-backed execution to the source handler", func() {
		review := f.hold(f.review, f.reviewPlan)
		f.abortReview()
		f.discover()

		lease := f.lease("worker")
		op := f.claimed(lease, db.CancelExecution, executionSubject(review.Execution))
		err := f.inTx(func(tx db.Tx) error {
			_, err := f.factory.CancellationRunExecution(f.ctx, tx, lease, op)
			return err
		})
		Expect(err).To(MatchError(db.ErrRunCancellationExternalWork), "a closure's handoff-backed execution was not left to the source handler")
	})
})

func executionSubject(id executioncontrol.Identity) string {
	return fmt.Sprintf("%s/%d", id.ExecutionID, id.Fence)
}

// closureHandoffs is one running v2 Run with two producer builds: review,
// which is aborted over an unsettled handoff, and sibling.
type closureHandoffs struct {
	ctx                     context.Context
	factory                 db.PipelineRunFactory
	creation                db.RunCreation
	review, sibling         db.Build
	reviewPlan, siblingPlan atc.TaskPlan
	control                 *executioncontrol.AcknowledgementSigner
	capture                 *output.CaptureStatementSigner
	receipts                *output.ReceiptSigner
	controlKeys             hangaroutput.ControlKeyRing
	receiptKeys             hangaroutput.ReceiptKeyRing
	node                    closureNode
}

// closureNode answers for the one node every producer runs on. A producer
// never started until it starts, and executes until the node records its
// finish; a source-preserving stop of an executing producer only interrupts
// it, and the finish is the node's own later fact.
type closureNode struct {
	started     bool
	outcome     *executioncontrol.Acknowledgement
	interrupted int
}

func (n *closureNode) start() { n.started = true }

func (n *closureNode) classify(id executioncontrol.Identity) executioncontrol.ClassifyResult {
	result := executioncontrol.ClassifyResult{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id, Classification: executioncontrol.ClassificationNeverStarted}
	switch {
	case n.outcome != nil:
		result.Classification, result.Acknowledgement = executioncontrol.ClassificationAuthoritativeFinish, n.outcome
	case n.started:
		result.Classification = executioncontrol.ClassificationExecuting
	}
	return result
}

func (n *closureNode) stop(id executioncontrol.Identity) executioncontrol.RequestSourcePreservingStopResult {
	current := n.classify(id).Classification
	if current == executioncontrol.ClassificationExecuting {
		n.interrupted++
	}
	return executioncontrol.RequestSourcePreservingStopResult{ProtocolVersion: executioncontrol.ProtocolVersion, Identity: id, Accepted: !current.Terminal(), Classification: current}
}

func (n *closureNode) finish(f *closureHandoffs, r output.HandoffRecord, code int) {
	GinkgoHelper()
	ack, err := f.control.Sign(executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementFinish,
		Identity: r.Execution, ActivationEpoch: 1, LedgerSequence: 3, NodeUID: "node-uid", PodUID: "pod-uid",
		ProcessIdentity: "producer-process", ObservedAt: output.NewTimestamp(time.Now()),
		Outcome: &executioncontrol.ExitOutcome{ExitCode: code},
	})
	Expect(err).NotTo(HaveOccurred())
	n.outcome = &ack
}

func newClosureHandoffs() *closureHandoffs {
	GinkgoHelper()
	f := &closureHandoffs{ctx: context.Background()}
	consumer, err := db.HangarConsumerPrefixHeld("abort-handoff-test")
	Expect(err).NotTo(HaveOccurred())
	hangarActivateEpoch(f.ctx, db.NewHangarOutputRepository(consumer))
	_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
	Expect(err).NotTo(HaveOccurred())

	producer := func(result string) *atc.TaskStep {
		return &atc.TaskStep{
			Name: "produce", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: result, Output: "result"},
			Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
		}
	}
	template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "aborted-handoffs"}, atc.Config{
		Template: true,
		Jobs: atc.JobConfigs{
			{Name: "review", PlanSequence: []atc.Step{{Config: producer("findings")}}},
			{Name: "sibling", PlanSequence: []atc.Step{{Config: producer("notes")}}},
		},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	f.factory = db.NewPipelineRunFactory(dbConn, lockFactory)
	Expect(f.inTx(func(tx db.Tx) error {
		var err error
		f.creation, err = f.factory.CreateRunInTx(f.ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarEpoch: 1})
		return err
	})).To(Succeed())
	for _, build := range f.creation.EntryBuilds {
		switch build.JobName() {
		case "review":
			f.review = build
		case "sibling":
			f.sibling = build
		}
	}
	Expect(f.review).NotTo(BeNil())
	Expect(f.sibling).NotTo(BeNil())
	for _, job := range f.creation.Config.Jobs {
		step := job.PlanSequence[0].Config.(*atc.TaskStep)
		plan := atc.TaskPlan{Name: step.Name, TaskID: step.TaskID, RunResult: step.RunResult, Config: step.Config}
		switch job.Name {
		case "review":
			f.reviewPlan = plan
		case "sibling":
			f.siblingPlan = plan
		}
	}

	// One node key signs both control statements, as the control key ring
	// pins one verification key per epoch.
	public, private, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	f.control, err = executioncontrol.NewAcknowledgementSigner(private)
	Expect(err).NotTo(HaveOccurred())
	f.capture, err = output.NewCaptureStatementSigner(private)
	Expect(err).NotTo(HaveOccurred())
	f.controlKeys = hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}}
	receiptPublic, receiptPrivate, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	f.receipts, err = output.NewReceiptSigner("receipt-key-1", 1, receiptPrivate, output.ClockFunc(time.Now))
	Expect(err).NotTo(HaveOccurred())
	f.receiptKeys = hangaroutput.ReceiptKeyRing{ActiveKeyID: "receipt-key-1", ActivationEpoch: 1, Keys: []hangaroutput.ReceiptKeyEntry{{ID: "receipt-key-1", Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(receiptPublic)}}}
	return f
}

func (f *closureHandoffs) inTx(fn func(db.Tx) error) error {
	tx, err := dbConn.Begin()
	if err != nil {
		return err
	}
	defer db.Rollback(tx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (f *closureHandoffs) outputs() *db.RunOutputRepository {
	GinkgoHelper()
	verifier, err := f.receiptKeys.SignatureVerifier(output.ClockFunc(time.Now))
	Expect(err).NotTo(HaveOccurred())
	return db.NewRunOutputRepository(db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent()), f.controlKeys, verifier)
}

func (f *closureHandoffs) incarnation(r output.HandoffRecord) output.SourceIncarnation {
	return output.SourceIncarnation{ExecutionID: r.Execution.ExecutionID, NodeUID: "node-uid", HandleGeneration: 1, Output: r.Output}
}

// hold admits a build's producer, reserves its source on the node and
// retains the node's signed hold: an output handoff nothing has settled.
func (f *closureHandoffs) hold(build db.Build, plan atc.TaskPlan) output.HandoffRecord {
	GinkgoHelper()
	var r output.HandoffRecord
	Expect(f.inTx(func(tx db.Tx) error {
		var err error
		r, err = f.factory.PredeclareOutputTask(f.ctx, tx, build.ID(), plan, 1, time.Hour, "node", "node-uid")
		return err
	})).To(Succeed())
	Expect(f.inTx(func(tx db.Tx) error { return f.factory.RequestOutputSource(f.ctx, tx, build.ID(), plan, 1) })).To(Succeed())
	incarnation := f.incarnation(r)
	Expect(f.inTx(func(tx db.Tx) error {
		return f.factory.RecordOutputSource(f.ctx, tx, build.ID(), plan, output.ReservedIncarnation{
			ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: 1,
			HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, NodeUID: "node-uid", Incarnation: incarnation,
			Directory: string(r.Execution.ExecutionID) + ".1/" + string(r.Output), LedgerSequence: 1,
			ObservedAt: output.NewTimestamp(time.Now()),
		}, "node")
	})).To(Succeed())
	hold, err := f.capture.SignCapture(output.CaptureAcknowledgement{
		ProtocolVersion: output.ProtocolVersion, Kind: output.CaptureHoldAcknowledged, Execution: r.Execution,
		ActivationEpoch: 1, LedgerSequence: 2, NodeUID: "node-uid", PodUID: "pod-uid",
		HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID, Incarnation: incarnation,
		ObservedAt: output.NewTimestamp(time.Now()),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(f.inTx(func(tx db.Tx) error { return f.outputs().AcknowledgeSourceHold(f.ctx, tx, hold) })).To(Succeed())
	return r
}

// reserveCapture selects capture for a producer that finished successfully:
// Stage 2, before anything is published.
func (f *closureHandoffs) reserveCapture(r output.HandoffRecord) output.ReservationID {
	GinkgoHelper()
	finish, err := f.control.Sign(executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementFinish,
		Identity: r.Execution, ActivationEpoch: 1, LedgerSequence: 3, NodeUID: "node-uid", PodUID: "pod-uid",
		ProcessIdentity: "producer-process", ObservedAt: output.NewTimestamp(time.Now()),
		Outcome: &executioncontrol.ExitOutcome{ExitCode: 0},
	})
	Expect(err).NotTo(HaveOccurred())
	var reservation output.ReservationID
	Expect(f.inTx(func(tx db.Tx) error {
		var err error
		reservation, err = f.outputs().CommitCaptureReservation(f.ctx, tx, output.SuccessfulFinishDisposition{
			ProtocolVersion: output.ProtocolVersion, Disposition: output.DispositionCapture, Execution: r.Execution,
			ActivationEpoch: 1, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID,
			ProducerCheckpointID: output.OpaqueID(uuid.NewString()), Output: r.Output, CaptureFence: 1,
			CaptureDeadline: r.CaptureDeadline, FinishAcknowledgement: finish,
		})
		return err
	})).To(Succeed())
	return reservation
}

// publishSibling carries the sibling's capture through publication, a
// verified receipt and its source release, which registers its candidate,
// and finishes the sibling succeeded.
func (f *closureHandoffs) publishSibling() {
	GinkgoHelper()
	r := f.hold(f.sibling, f.siblingPlan)
	reservation := f.reserveCapture(r)
	var checkpoint string
	Expect(dbConn.QueryRow(`SELECT producer_checkpoint_id FROM pipeline_run_output_finishes WHERE handoff_id=$1`, string(r.HandoffID)).Scan(&checkpoint)).To(Succeed())
	ref := hangar.TreeRef{Scope: "team-a", Digest: hangar.Digest("sha256:" + closureHex()), Generation: 1}
	Expect(f.inTx(func(tx db.Tx) error {
		repository := f.outputs()
		if _, err := repository.AcquireCaptureLease(f.ctx, tx, reservation, uuid.NewString(), output.MinLeaseTerm); err != nil {
			return err
		}
		if err := repository.ResolveLogicalReservation(f.ctx, tx, output.LogicalResolution{
			ProtocolVersion: output.ProtocolVersion, Execution: r.Execution, ActivationEpoch: 1,
			HandoffID: r.HandoffID, ReservationID: reservation, CaptureFence: 1,
			Scope: ref.Scope, Digest: ref.Digest, LogicalBytes: 4096, ResolvedAt: output.NewTimestamp(time.Now()),
		}); err != nil {
			return err
		}
		return repository.RecordFirstObjectCreate(f.ctx, tx, reservation, 1)
	})).To(Succeed())
	nonce, issuedAt := hangarIssueChallenge(r.HandoffID, reservation, ref)
	admission := hangarAdmissionFor(r.HandoffID, r.Execution, reservation, ref, nonce, issuedAt)
	admission.Receipt.Claims.ProducerCheckpointID = output.OpaqueID(checkpoint)
	admission.Receipt.Claims.Output = r.Output
	admission.Receipt.Claims.Incarnation.Output = r.Output
	receipt, err := f.receipts.Sign(admission.Receipt.Claims)
	Expect(err).NotTo(HaveOccurred())
	admission.Receipt = receipt
	Expect(f.inTx(func(tx db.Tx) error { return f.outputs().RegisterReceipt(f.ctx, tx, admission) })).To(Succeed())

	var intent string
	Expect(dbConn.QueryRow(`SELECT release_intent_id FROM hangar_capture_reservations WHERE reservation_id=$1`, string(reservation)).Scan(&intent)).To(Succeed())
	release := f.release(r, output.DispositionCapture, output.ReleaseIntentID(intent))
	Expect(f.inTx(func(tx db.Tx) error { return f.outputs().AcknowledgeCaptureRelease(f.ctx, tx, release) })).To(Succeed())
	Expect(f.sibling.Finish(db.BuildStatusSucceeded)).To(Succeed())
	Expect(f.activeClaims()).To(Equal(1))
}

func (f *closureHandoffs) release(r output.HandoffRecord, disposition output.Disposition, intent output.ReleaseIntentID) output.ReleaseAcknowledgement {
	GinkgoHelper()
	ack, err := f.capture.SignRelease(output.ReleaseAcknowledgement{
		ProtocolVersion: output.ProtocolVersion, Disposition: disposition, Execution: r.Execution,
		ActivationEpoch: 1, HandoffID: r.HandoffID, SourceHoldID: r.SourceHoldID,
		ReleaseIntentID: intent, Incarnation: f.incarnation(r),
		LedgerSequence: 4, ObservedAt: output.NewTimestamp(time.Now()),
	})
	Expect(err).NotTo(HaveOccurred())
	return ack
}

// abortReview aborts the review build over its unsettled handoff, which
// records its build closure and leaves the Run running.
func (f *closureHandoffs) abortReview() {
	GinkgoHelper()
	Expect(f.review.MarkAsAborted()).To(Succeed())
	Expect(f.review.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))
	var closures int
	Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_build_closures WHERE build_id=$1 AND closed_at IS NULL`, f.review.ID()).Scan(&closures)).To(Succeed())
	Expect(closures).To(Equal(1))
	Expect(f.cancellationRequested()).To(BeFalse())
}

func (f *closureHandoffs) lease(owner string) db.RunCancellationLease {
	GinkgoHelper()
	var lease db.RunCancellationLease
	Expect(f.inTx(func(tx db.Tx) error {
		var owned bool
		var err error
		lease, owned, err = f.factory.ClaimRunCancellationLease(f.ctx, tx, owner, time.Minute)
		Expect(owned).To(BeTrue())
		return err
	})).To(Succeed())
	return lease
}

// discover runs one bounded discovery pass and returns every recorded
// operation as kind -> subjects.
func (f *closureHandoffs) discover() map[db.RunCancellationKind][]string {
	GinkgoHelper()
	lease := f.lease("worker")
	Expect(f.inTx(func(tx db.Tx) error {
		_, err := f.factory.DiscoverRunCancellation(f.ctx, tx, lease, f.creation.Run.ID(), db.RunCancellationOperationLimit)
		return err
	})).To(Succeed())
	rows, err := dbConn.Query(`SELECT kind,subject FROM pipeline_run_cancellation_operations WHERE run_id=$1`, f.creation.Run.ID())
	Expect(err).NotTo(HaveOccurred())
	defer db.Close(rows)
	operations := map[db.RunCancellationKind][]string{}
	for rows.Next() {
		var kind, subject string
		Expect(rows.Scan(&kind, &subject)).To(Succeed())
		operations[db.RunCancellationKind(kind)] = append(operations[db.RunCancellationKind(kind)], subject)
	}
	Expect(rows.Err()).NotTo(HaveOccurred())
	return operations
}

// claimed makes one operation, discovered or not, the lease's current claim,
// as ClaimRunCancellationOperation would, so only the gate under test can
// refuse it.
func (f *closureHandoffs) claimed(lease db.RunCancellationLease, kind db.RunCancellationKind, subject string) db.RunCancellationOperation {
	GinkgoHelper()
	op := db.RunCancellationOperation{RunID: f.creation.Run.ID(), Kind: kind, Subject: subject, Attempt: 1, WorkerEpoch: lease.Epoch}
	Expect(dbConn.QueryRow(`INSERT INTO pipeline_run_cancellation_operations(run_id,kind,subject) VALUES($1,$2,$3)
 ON CONFLICT (run_id,kind,subject) DO UPDATE SET run_id=EXCLUDED.run_id RETURNING id`, op.RunID, string(kind), subject).Scan(&op.ID)).To(Succeed())
	_, err := dbConn.Exec(`UPDATE pipeline_run_cancellation_operations SET attempt_count=1,worker_epoch=$2,claimed=true,debt='interrupted',next_at=now()+interval '1 minute' WHERE id=$1`, op.ID, lease.Epoch)
	Expect(err).NotTo(HaveOccurred())
	return op
}

// pass is one cancellation-worker pass: the source handler's database
// operations for handoff-backed work, with the node replaced by closureNode,
// and finality for the build. Held kinds answer pending, as a slow node
// would. Backed-off operations are made due first, so each pass sees
// everything.
func (f *closureHandoffs) pass(held ...db.RunCancellationKind) {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=now() - interval '1 second' WHERE completed_at IS NULL`)
	Expect(err).NotTo(HaveOccurred())
	lease := f.lease("worker")
	for visit := 0; visit < db.RunCancellationOperationLimit; visit++ {
		var pending []int
		Expect(f.inTx(func(tx db.Tx) error {
			var err error
			pending, err = f.factory.PendingRunCancellations(f.ctx, tx, lease, 1)
			return err
		})).To(Succeed())
		if len(pending) == 0 {
			return
		}
		var op db.RunCancellationOperation
		var found bool
		Expect(f.inTx(func(tx db.Tx) error {
			if _, err := f.factory.DiscoverRunCancellation(f.ctx, tx, lease, pending[0], db.RunCancellationOperationLimit); err != nil {
				return err
			}
			var err error
			op, found, err = f.factory.ClaimRunCancellationOperation(f.ctx, tx, lease, pending[0])
			return err
		})).To(Succeed())
		if !found {
			return
		}
		debt := db.CancellationPending
		switch {
		case slices.Contains(held, op.Kind):
		case op.Kind == db.CancelBuild:
			debt, _ = f.factory.ExecuteCancellationFinality(f.ctx, lease, op)
		default:
			debt = f.source(lease, op)
		}
		Expect(f.inTx(func(tx db.Tx) error { return f.factory.RecordRunCancellationProgress(f.ctx, tx, lease, op, debt) })).To(Succeed())
	}
}

// source mirrors runs.CancellationSources: classify first, then close the
// execution on the node's exact evidence, then settle the hold.
func (f *closureHandoffs) source(lease db.RunCancellationLease, op db.RunCancellationOperation) db.RunCancellationDebt {
	GinkgoHelper()
	var in db.RunCancellationSource
	Expect(f.inTx(func(tx db.Tx) error {
		var err error
		in, err = f.factory.CancellationOutputTask(f.ctx, tx, lease, op)
		return err
	})).To(Succeed(), "the build closure's %s operation was refused", op.Kind)
	r := in.Task.Record
	repository := f.outputs()
	record := func(fn func(db.Tx) error) db.RunCancellationDebt {
		err := f.inTx(func(tx db.Tx) error {
			if err := fn(tx); err != nil {
				return err
			}
			return f.factory.CheckCancellationOperation(f.ctx, tx, lease, op)
		})
		if errors.Is(err, atc.ErrRunOutputPending) {
			return db.CancellationPending
		}
		Expect(err).NotTo(HaveOccurred())
		return db.CancellationDone
	}
	observe := func() db.RunOutputCancellationEvidence {
		return db.RunOutputCancellationEvidence{NodeUID: "node-uid", Execution: f.node.classify(r.Execution)}
	}
	switch op.Kind {
	case db.CancelHandoff:
		if in.Classified {
			return db.CancellationDone
		}
		return record(func(tx db.Tx) error {
			return repository.RecordCancellationClassification(f.ctx, tx, lease, r.HandoffID, observe())
		})
	case db.CancelExecution:
		if !in.Classified {
			return db.CancellationPending
		}
		evidence := observe()
		if !evidence.Execution.Classification.Authoritative() {
			stop := f.node.stop(r.Execution)
			if evidence = observe(); evidence.Execution.Classification == executioncontrol.ClassificationNeverStarted {
				evidence.StartClosure = &stop
			}
		}
		return record(func(tx db.Tx) error {
			return repository.RecordCancellationEvidence(f.ctx, tx, lease, r.HandoffID, evidence)
		})
	default:
		if !in.Classified {
			return db.CancellationPending
		}
		if debt := record(func(tx db.Tx) error {
			_, err := repository.CancelOrSettle(f.ctx, tx, r.HandoffID)
			return err
		}); debt != db.CancellationDone {
			return debt
		}
		var current output.HandoffRecord
		Expect(f.inTx(func(tx db.Tx) error {
			var err error
			current, err = repository.LoadHandoffRecord(f.ctx, tx, r.HandoffID)
			return err
		})).To(Succeed())
		if current.Disposition != nil && !current.ReleaseAcknowledged {
			ack := f.release(current, *current.Disposition, current.ReleaseIntentID)
			if debt := record(func(tx db.Tx) error {
				return repository.AcknowledgePreReservationCancelRelease(f.ctx, tx, ack)
			}); debt != db.CancellationDone {
				return debt
			}
		}
		var status output.HandoffStatus
		Expect(f.inTx(func(tx db.Tx) error {
			var err error
			status, err = repository.ClassifyHandoff(f.ctx, tx, r.HandoffID)
			return err
		})).To(Succeed())
		if !status.Settled {
			return db.CancellationPending
		}
		return db.CancellationDone
	}
}

func (f *closureHandoffs) classification(r output.HandoffRecord) string {
	GinkgoHelper()
	var classification string
	Expect(dbConn.QueryRow(`SELECT coalesce((SELECT classification FROM pipeline_run_output_cancellation_classifications WHERE handoff_id=$1),'')`, string(r.HandoffID)).Scan(&classification)).To(Succeed())
	return classification
}

func (f *closureHandoffs) evidence(r output.HandoffRecord) string {
	GinkgoHelper()
	var classification string
	Expect(dbConn.QueryRow(`SELECT coalesce((SELECT classification FROM pipeline_run_output_cancellation_evidence WHERE handoff_id=$1),'')`, string(r.HandoffID)).Scan(&classification)).To(Succeed())
	return classification
}

func (f *closureHandoffs) released(r output.HandoffRecord) bool {
	GinkgoHelper()
	var released bool
	Expect(dbConn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_output_releases WHERE handoff_id=$1)`, string(r.HandoffID)).Scan(&released)).To(Succeed())
	return released
}

// releasedAfterEvidence reads the order from the Hangar disposition that
// records the release intent: it may only follow the retained evidence.
func (f *closureHandoffs) releasedAfterEvidence(r output.HandoffRecord) bool {
	GinkgoHelper()
	var ordered bool
	Expect(dbConn.QueryRow(`SELECT e.recorded_at <= x.intent_recorded_at FROM pipeline_run_output_cancellation_evidence e
 JOIN hangar_pre_reservation_cancel_dispositions x USING(handoff_id) WHERE handoff_id=$1`, string(r.HandoffID)).Scan(&ordered)).To(Succeed())
	return ordered
}

func (f *closureHandoffs) closureClosed() bool {
	GinkgoHelper()
	var closed bool
	Expect(dbConn.QueryRow(`SELECT closed_at IS NOT NULL FROM pipeline_run_build_closures WHERE build_id=$1`, f.review.ID()).Scan(&closed)).To(Succeed())
	return closed
}

func (f *closureHandoffs) closedAfterEveryOperation() bool {
	GinkgoHelper()
	var ordered bool
	Expect(dbConn.QueryRow(`SELECT bool_and(op.completed_at IS NOT NULL AND op.completed_at <= bc.closed_at)
 FROM pipeline_run_cancellation_operations op JOIN pipeline_run_build_closures bc USING(run_id) WHERE bc.build_id=$1`, f.review.ID()).Scan(&ordered)).To(Succeed())
	return ordered
}

func (f *closureHandoffs) buildState(b db.Build) (bool, string) {
	GinkgoHelper()
	var completed bool
	var status string
	Expect(dbConn.QueryRow(`SELECT completed,status FROM builds WHERE id=$1`, b.ID()).Scan(&completed, &status)).To(Succeed())
	return completed, status
}

func (f *closureHandoffs) finalize() bool {
	GinkgoHelper()
	var ready bool
	Expect(f.inTx(func(tx db.Tx) error {
		var err error
		ready, err = f.factory.FinalizeOutputRun(f.ctx, tx, f.creation.Run.ID())
		return err
	})).To(Succeed())
	return ready
}

// schedulerCaughtUp stands in for the scheduler, which no db test runs.
func (f *closureHandoffs) schedulerCaughtUp() {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE jobs SET last_scheduled=schedule_requested WHERE pipeline_id=(SELECT id FROM pipelines WHERE pipeline_run_id=$1)`, f.creation.Run.ID())
	Expect(err).NotTo(HaveOccurred())
}

func (f *closureHandoffs) runStatus() string {
	GinkgoHelper()
	var status string
	Expect(dbConn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, f.creation.Run.ID()).Scan(&status)).To(Succeed())
	return status
}

func (f *closureHandoffs) cancellationRequested() bool {
	GinkgoHelper()
	var requested bool
	Expect(dbConn.QueryRow(`SELECT cancel_requested_at IS NOT NULL FROM pipeline_runs WHERE id=$1`, f.creation.Run.ID()).Scan(&requested)).To(Succeed())
	return requested
}

func (f *closureHandoffs) activeClaims() int {
	GinkgoHelper()
	var active int
	Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_output_candidates c JOIN pipeline_run_output_starts s USING(handoff_id)
 JOIN hangar_claims claim USING(claim_id) WHERE s.run_id=$1 AND claim.released_at IS NULL`, f.creation.Run.ID()).Scan(&active)).To(Succeed())
	return active
}

func closureHex() string {
	GinkgoHelper()
	var b [32]byte
	_, err := rand.Read(b[:])
	Expect(err).NotTo(HaveOccurred())
	return hex.EncodeToString(b[:])
}
