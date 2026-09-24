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
