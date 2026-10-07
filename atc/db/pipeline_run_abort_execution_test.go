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
		GinkgoHelper()
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

	It("closes, completes and reclaims a Run whose Hangar output plane was taken out of service", func() {
		Expect(other.Finish(db.BuildStatusSucceeded)).To(Succeed())
		execution := abortOverOpenExecution()
		finishExecution(build, execution)
		_, err := db.SetHangarEnabled(context.Background(), dbConn, false)
		Expect(err).NotTo(HaveOccurred())

		closurePass("worker")
		closurePass("worker")
		Expect(closureClosed()).To(BeTrue(), "taking Hangar out of service stranded the build closure")
		schedulerCaughtUp()
		Expect(finalize()).To(BeTrue(), "taking Hangar out of service stranded ordinary completion")
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

	It("lets a rerun finish while the aborted build it reruns is still closing", func() {
		abortOverOpenExecution()
		pipeline, found, err := build.Pipeline()
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		job, found, err := pipeline.Job(build.JobName())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		rerun, err := job.RerunBuild(build, "rerun")
		Expect(err).NotTo(HaveOccurred())

		Expect(rerun.Finish(db.BuildStatusSucceeded)).To(Succeed(), "a rerun finishing before its original broke the job's completion bookkeeping")
		var latest int
		Expect(dbConn.QueryRow(`SELECT latest_completed_build_id FROM jobs WHERE id=$1`, job.ID()).Scan(&latest)).To(Succeed())
		Expect(latest).To(Equal(rerun.ID()), "the job's latest completed build is not the rerun that completed")
		Expect(closureClosed()).To(BeFalse())
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

// An aborted Run build whose capture is unsettled -- its producer never
// started, or is still executing -- is closed by its build closure: the
// capture is discarded by the database-only CancelHandoff operation and the
// capturing execution is closed through the generic execution closure, like
// any other execution, for that build's own subjects only. The node is
// closureNode, which answers as the exact node would.
var _ = Describe("Closing an aborted Run build's unsettled capture", func() {
	var f *closureCaptures

	BeforeEach(func() {
		f = newClosureCaptures()
	})

	It("discovers only the aborted build's capture, execution and build", func() {
		sibling := f.start(f.sibling, f.siblingPlan)
		review := f.start(f.review, f.reviewPlan)
		f.node.start(f, review)
		f.abortReview()

		operations := f.discover()
		Expect(operations).To(HaveKeyWithValue(db.CancelHandoff, ConsistOf(review.Capture.String())))
		Expect(operations).To(HaveKeyWithValue(db.CancelExecution, ConsistOf(executionSubject(review.Identity))))
		Expect(operations).To(HaveKeyWithValue(db.CancelBuild, ConsistOf(strconv.Itoa(f.review.ID()))))
		Expect(operations).NotTo(HaveKey(db.CancelCapture), "a capture is one row; nothing discovers a handoff-era capture kind")
		Expect(operations).NotTo(HaveKey(db.CancelSourceHold), "a capture is one row; nothing discovers a handoff-era hold kind")
		for kind, subjects := range operations {
			Expect(subjects).NotTo(ContainElements(sibling.Capture.String(), executionSubject(sibling.Identity)),
				"%s reached another build's live capture", kind)
		}

		// Each handler accepts its operation without Run cancellation.
		lease := f.lease("worker")
		handoff := f.claimed(lease, db.CancelHandoff, operations[db.CancelHandoff][0])
		debt, err := f.factory.ExecuteCancellationFinality(f.ctx, lease, handoff)
		Expect(err).NotTo(HaveOccurred(), "the build closure's capture operation was refused")
		Expect(debt).To(Equal(db.CancellationDone))
		execution := f.claimed(lease, db.CancelExecution, operations[db.CancelExecution][0])
		Expect(f.inTx(func(tx db.Tx) error {
			_, err := f.factory.CancellationRunExecution(f.ctx, tx, lease, execution)
			return err
		})).To(Succeed(), "the build closure's execution operation was refused")
		Expect(f.cancellationRequested()).To(BeFalse())

		// An operation of a handoff-era kind recorded before the upgrade has
		// nothing left to do.
		for _, kind := range []db.RunCancellationKind{db.CancelCapture, db.CancelSourceHold} {
			debt, err := f.factory.ExecuteCancellationFinality(f.ctx, lease, f.claimed(lease, kind, uuid.NewString()))
			Expect(err).NotTo(HaveOccurred())
			Expect(debt).To(Equal(db.CancellationDone), "a %s operation was left owing work", kind)
		}
	})

	It("discards a never-started producer's capture, and only then lets the Run complete aborted with every unselected claim released", func() {
		f.publishSibling()
		review := f.start(f.review, f.reviewPlan)
		f.abortReview()

		f.pass(db.CancelBuild)
		f.pass(db.CancelBuild)
		Expect(f.captureState(review)).To(Equal([2]string{"discarded", output.DiscardBuildAborted}))
		Expect(f.executionClosure(review)).To(Equal("never_started"))
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

	It("interrupts an executing producer and closes its execution only on the node's exact finish", func() {
		review := f.start(f.review, f.reviewPlan)
		f.node.start(f, review)
		f.abortReview()

		for pass := 0; pass < 3; pass++ {
			f.pass()
		}
		Expect(f.captureState(review)).To(Equal([2]string{"discarded", output.DiscardBuildAborted}),
			"a pending capture of an aborted build is discarded whatever its producer is doing")
		Expect(f.node.interrupted).To(BeNumerically(">", 0), "the executing producer was never interrupted")
		Expect(f.executionClosure(review)).To(BeEmpty(), "an interruption request was taken for the producer's outcome")
		completed, _ := f.buildState(f.review)
		Expect(completed).To(BeFalse())
		Expect(f.closureClosed()).To(BeFalse())

		f.node.finish(f, review, 143)
		for pass := 0; pass < 3 && !f.closureClosed(); pass++ {
			f.pass()
		}
		Expect(f.executionClosure(review)).To(Equal("authoritative_finish"))
		Expect(f.closureClosed()).To(BeTrue())
		Expect(f.runStatus()).To(Equal(string(atc.RunStatusRunning)))
	})

	It("refuses capture work on another build's capture without Run cancellation", func() {
		sibling := f.start(f.sibling, f.siblingPlan)
		f.start(f.review, f.reviewPlan)
		f.abortReview()
		f.discover()

		lease := f.lease("worker")
		op := f.claimed(lease, db.CancelHandoff, sibling.Capture.String())
		debt, err := f.factory.ExecuteCancellationFinality(f.ctx, lease, op)
		Expect(err).To(MatchError(db.ErrRunCancellationProgressStale), "a build closure reached another build's capture")
		Expect(debt).To(Equal(db.CancellationConflict))
		Expect(f.captureState(sibling)).To(Equal([2]string{"pending", ""}))
	})

	It("closes its capturing execution through the generic execution closure", func() {
		review := f.start(f.review, f.reviewPlan)
		f.abortReview()
		f.discover()

		lease := f.lease("worker")
		op := f.claimed(lease, db.CancelExecution, executionSubject(review.Identity))
		var in db.RunCancellationExecution
		Expect(f.inTx(func(tx db.Tx) error {
			var err error
			in, err = f.factory.CancellationRunExecution(f.ctx, tx, lease, op)
			return err
		})).To(Succeed(), "a closure's capturing execution was not resolved by the execution handler")
		Expect(in.Admission.Identity).To(Equal(review.Identity))
		Expect(in.Admission.Capture).To(Equal(review.Capture))
		Expect(in.Closed).To(BeFalse())
	})
})

func executionSubject(id executioncontrol.Identity) string {
	return fmt.Sprintf("%s/%d", id.ExecutionID, id.Fence)
}

// closureCaptures is one running v2 Run with two producer builds: review,
// which is aborted over an unsettled capture, and sibling.
type closureCaptures struct {
	ctx                     context.Context
	factory                 db.PipelineRunFactory
	creation                db.RunCreation
	review, sibling         db.Build
	reviewPlan, siblingPlan atc.TaskPlan
	control                 *executioncontrol.AcknowledgementSigner
	controlKeys             hangaroutput.ControlKeyRing
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

// start starts the producer on the node, and the node's signed start is
// retained, as the execution starter does.
func (n *closureNode) start(f *closureCaptures, a db.RunExecutionAdmission) {
	GinkgoHelper()
	n.started = true
	f.witness(a, executioncontrol.AcknowledgementStart, nil)
}

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

func (n *closureNode) finish(f *closureCaptures, a db.RunExecutionAdmission, code int) {
	GinkgoHelper()
	ack := f.sign(a, executioncontrol.AcknowledgementFinish, &executioncontrol.ExitOutcome{ExitCode: code})
	n.outcome = &ack
}

func newClosureCaptures() *closureCaptures {
	GinkgoHelper()
	f := &closureCaptures{ctx: context.Background()}
	consumer, err := db.HangarConsumerPrefixHeld("abort-capture-test")
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
	template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "aborted-captures"}, atc.Config{
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

	public, private, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	f.control, err = executioncontrol.NewAcknowledgementSigner(private)
	Expect(err).NotTo(HaveOccurred())
	f.controlKeys = hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}}
	return f
}

func (f *closureCaptures) inTx(fn func(db.Tx) error) error {
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

// start admits a build's producer as the output starter does: a pending
// capture and the Run's link to it, then the capturing execution under the
// capture's own identity. Nothing has settled the capture.
func (f *closureCaptures) start(build db.Build, plan atc.TaskPlan) db.RunExecutionAdmission {
	GinkgoHelper()
	var admission db.RunExecutionAdmission
	Expect(f.inTx(func(tx db.Tx) error {
		capture, err := f.factory.StartRunCapture(f.ctx, tx, build.ID(), plan, 1, time.Hour, "node", "node-uid")
		if err != nil {
			return err
		}
		var owned bool
		admission, owned, err = f.factory.AdmitRunExecution(f.ctx, tx, db.RunExecutionRequest{
			BuildID: build.ID(), PlanID: atc.PlanID("produce-" + plan.TaskID), Kind: db.ContainerTypeTask,
			Epoch: 1, NodeName: "node", NodeUID: "node-uid", Capture: capture.Key(),
		})
		Expect(owned).To(BeTrue())
		return err
	})).To(Succeed())
	Expect(admission.Identity.ExecutionID).To(Equal(admission.Capture.ExecutionID),
		"a capturing execution was admitted under an identity other than its capture's")
	return admission
}

func (f *closureCaptures) sign(a db.RunExecutionAdmission, kind executioncontrol.AcknowledgementKind, outcome *executioncontrol.ExitOutcome) executioncontrol.Acknowledgement {
	GinkgoHelper()
	sequence := executioncontrol.LedgerSequence(1)
	if kind == executioncontrol.AcknowledgementFinish {
		sequence = 2
	}
	ack, err := f.control.Sign(executioncontrol.Acknowledgement{
		ProtocolVersion: executioncontrol.ProtocolVersion, Kind: kind,
		Identity: a.Identity, ActivationEpoch: 1, LedgerSequence: sequence, NodeUID: "node-uid", PodUID: "pod-uid",
		ProcessIdentity: "producer-process", ObservedAt: output.NewTimestamp(time.Now()), Outcome: outcome,
	})
	Expect(err).NotTo(HaveOccurred())
	return ack
}

// witness retains one of the node's signed statements about an execution.
func (f *closureCaptures) witness(a db.RunExecutionAdmission, kind executioncontrol.AcknowledgementKind, outcome *executioncontrol.ExitOutcome) {
	GinkgoHelper()
	ack := f.sign(a, kind, outcome)
	Expect(f.inTx(func(tx db.Tx) error {
		return f.factory.RecordRunExecutionWitness(f.ctx, tx, a.BuildID, a.PlanID, ack, f.controlKeys)
	})).To(Succeed())
}

// publishSibling carries the sibling's capture through publication, which
// takes its candidate claim, closes its execution on the node's finish, and
// finishes the sibling succeeded.
func (f *closureCaptures) publishSibling() {
	GinkgoHelper()
	a := f.start(f.sibling, f.siblingPlan)
	f.witness(a, executioncontrol.AcknowledgementStart, nil)
	f.witness(a, executioncontrol.AcknowledgementFinish, &executioncontrol.ExitOutcome{ExitCode: 0})
	repository := db.NewHangarOutputRepository(db.HangarConsumerPrefixForComponent())
	Expect(f.inTx(func(tx db.Tx) error {
		if _, err := repository.CASPendingToPublishing(f.ctx, tx, a.Capture, "pod-uid", "team-a",
			hangar.Digest("sha256:"+closureHex())); err != nil {
			return err
		}
		_, err := repository.CASPublishingToPublished(f.ctx, tx, output.PublishedCapture{
			Key: a.Capture, Generation: 1, Metageneration: 1, ActivationEpoch: 1,
		})
		return err
	})).To(Succeed())
	Expect(f.sibling.Finish(db.BuildStatusSucceeded)).To(Succeed())
	Expect(f.activeClaims()).To(Equal(1))
}

// abortReview aborts the review build over its unsettled capture, which
// records its build closure and leaves the Run running.
func (f *closureCaptures) abortReview() {
	GinkgoHelper()
	Expect(f.review.MarkAsAborted()).To(Succeed())
	Expect(f.review.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))
	var closures int
	Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_build_closures WHERE build_id=$1 AND closed_at IS NULL`, f.review.ID()).Scan(&closures)).To(Succeed())
	Expect(closures).To(Equal(1))
	Expect(f.cancellationRequested()).To(BeFalse())
}

func (f *closureCaptures) lease(owner string) db.RunCancellationLease {
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
func (f *closureCaptures) discover() map[db.RunCancellationKind][]string {
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
func (f *closureCaptures) claimed(lease db.RunCancellationLease, kind db.RunCancellationKind, subject string) db.RunCancellationOperation {
	GinkgoHelper()
	op := db.RunCancellationOperation{RunID: f.creation.Run.ID(), Kind: kind, Subject: subject, Attempt: 1, WorkerEpoch: lease.Epoch}
	Expect(dbConn.QueryRow(`INSERT INTO pipeline_run_cancellation_operations(run_id,kind,subject) VALUES($1,$2,$3)
 ON CONFLICT (run_id,kind,subject) DO UPDATE SET run_id=EXCLUDED.run_id RETURNING id`, op.RunID, string(kind), subject).Scan(&op.ID)).To(Succeed())
	_, err := dbConn.Exec(`UPDATE pipeline_run_cancellation_operations SET attempt_count=1,worker_epoch=$2,claimed=true,debt='interrupted',next_at=now()+interval '1 minute' WHERE id=$1`, op.ID, lease.Epoch)
	Expect(err).NotTo(HaveOccurred())
	return op
}

// pass is one cancellation-worker pass: the database-only finality for the
// capture and the build, and the execution handler for the capturing
// execution with the node replaced by closureNode. Held kinds answer pending,
// as a slow node would. Backed-off operations are made due first, so each
// pass sees everything.
func (f *closureCaptures) pass(held ...db.RunCancellationKind) {
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
		case op.Kind == db.CancelExecution:
			debt = f.execution(lease, op)
		default:
			debt, _ = f.factory.ExecuteCancellationFinality(f.ctx, lease, op)
		}
		Expect(f.inTx(func(tx db.Tx) error { return f.factory.RecordRunCancellationProgress(f.ctx, tx, lease, op, debt) })).To(Succeed())
	}
}

// execution mirrors runs.CancellationExecutions: resolve the execution, and
// close it on the node's exact evidence -- a verified finish, or a
// source-preserving stop of a producer that never started.
func (f *closureCaptures) execution(lease db.RunCancellationLease, op db.RunCancellationOperation) db.RunCancellationDebt {
	GinkgoHelper()
	var in db.RunCancellationExecution
	Expect(f.inTx(func(tx db.Tx) error {
		var err error
		in, err = f.factory.CancellationRunExecution(f.ctx, tx, lease, op)
		return err
	})).To(Succeed(), "the build closure's execution operation was refused")
	if in.Closed {
		return db.CancellationDone
	}
	id := in.Admission.Identity
	evidence := db.RunOutputCancellationEvidence{NodeUID: "node-uid", Execution: f.node.classify(id)}
	if !evidence.Execution.Classification.Authoritative() {
		stop := f.node.stop(id)
		if evidence = (db.RunOutputCancellationEvidence{NodeUID: "node-uid", Execution: f.node.classify(id)}); evidence.Execution.Classification == executioncontrol.ClassificationNeverStarted {
			evidence.StartClosure = &stop
		}
	}
	err := f.inTx(func(tx db.Tx) error {
		return f.factory.RecordCancelledRunExecution(f.ctx, tx, lease, op, evidence, f.controlKeys)
	})
	if errors.Is(err, atc.ErrRunOutputPending) {
		return db.CancellationPending
	}
	Expect(err).NotTo(HaveOccurred())
	return db.CancellationDone
}

// captureState is the capture row's state and the reason it was discarded or
// failed, if it was.
func (f *closureCaptures) captureState(a db.RunExecutionAdmission) [2]string {
	GinkgoHelper()
	var state, reason string
	Expect(dbConn.QueryRow(`SELECT state, coalesce(error,'') FROM hangar_captures WHERE execution_id=$1 AND output_name=$2`,
		string(a.Capture.ExecutionID), string(a.Capture.Output)).Scan(&state, &reason)).To(Succeed())
	return [2]string{state, reason}
}

// executionClosure is the classification the execution was closed with, or
// empty while it is open.
func (f *closureCaptures) executionClosure(a db.RunExecutionAdmission) string {
	GinkgoHelper()
	var classification string
	Expect(dbConn.QueryRow(`SELECT coalesce((SELECT classification FROM pipeline_run_execution_closures WHERE execution_id=$1 AND execution_fence=$2),'')`,
		string(a.Identity.ExecutionID), int64(a.Identity.Fence)).Scan(&classification)).To(Succeed())
	return classification
}

func (f *closureCaptures) closureClosed() bool {
	GinkgoHelper()
	var closed bool
	Expect(dbConn.QueryRow(`SELECT closed_at IS NOT NULL FROM pipeline_run_build_closures WHERE build_id=$1`, f.review.ID()).Scan(&closed)).To(Succeed())
	return closed
}

func (f *closureCaptures) closedAfterEveryOperation() bool {
	GinkgoHelper()
	var ordered bool
	Expect(dbConn.QueryRow(`SELECT bool_and(op.completed_at IS NOT NULL AND op.completed_at <= bc.closed_at)
 FROM pipeline_run_cancellation_operations op JOIN pipeline_run_build_closures bc USING(run_id) WHERE bc.build_id=$1`, f.review.ID()).Scan(&ordered)).To(Succeed())
	return ordered
}

func (f *closureCaptures) buildState(b db.Build) (bool, string) {
	GinkgoHelper()
	var completed bool
	var status string
	Expect(dbConn.QueryRow(`SELECT completed,status FROM builds WHERE id=$1`, b.ID()).Scan(&completed, &status)).To(Succeed())
	return completed, status
}

func (f *closureCaptures) finalize() bool {
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
func (f *closureCaptures) schedulerCaughtUp() {
	GinkgoHelper()
	_, err := dbConn.Exec(`UPDATE jobs SET last_scheduled=schedule_requested WHERE pipeline_id=(SELECT id FROM pipelines WHERE pipeline_run_id=$1)`, f.creation.Run.ID())
	Expect(err).NotTo(HaveOccurred())
}

func (f *closureCaptures) runStatus() string {
	GinkgoHelper()
	var status string
	Expect(dbConn.QueryRow(`SELECT status FROM pipeline_runs WHERE id=$1`, f.creation.Run.ID()).Scan(&status)).To(Succeed())
	return status
}

func (f *closureCaptures) cancellationRequested() bool {
	GinkgoHelper()
	var requested bool
	Expect(dbConn.QueryRow(`SELECT cancel_requested_at IS NOT NULL FROM pipeline_runs WHERE id=$1`, f.creation.Run.ID()).Scan(&requested)).To(Succeed())
	return requested
}

// activeClaims counts the Run's candidate claims still active: the claims
// its captures' publications took.
func (f *closureCaptures) activeClaims() int {
	GinkgoHelper()
	var active int
	Expect(dbConn.QueryRow(`SELECT count(*) FROM pipeline_run_captures s
 JOIN hangar_claims claim ON claim.consumer_binding_id='capture:'||s.execution_id::text||'/'||s.output_name
 WHERE s.run_id=$1 AND claim.released_at IS NULL`, f.creation.Run.ID()).Scan(&active)).To(Succeed())
	return active
}

func closureHex() string {
	GinkgoHelper()
	var b [32]byte
	_, err := rand.Read(b[:])
	Expect(err).NotTo(HaveOccurred())
	return hex.EncodeToString(b[:])
}
