package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
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
// outcome -- has nothing left that would: its replays are refused admission,
// and finishing it is refused while the execution is open. Run cancellation is
// the one path that interrupts and closes an execution on node-attested
// evidence, and the Run aborts with the build anyway, so finishing such a build
// asks for it.
var _ = Describe("Finishing an aborted Run build with an unclosed execution", func() {
	var (
		ctx      context.Context
		factory  db.PipelineRunFactory
		creation db.RunCreation
		build    db.Build
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

	// admitStarted admits the build's task and retains the node's signed
	// start, leaving it with no closure, as a command still unaccounted for.
	admitStarted := func() db.RunExecutionAdmission {
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

	BeforeEach(func() {
		ctx = context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("abort-execution-test")
		Expect(err).NotTo(HaveOccurred())
		hangarActivateEpoch(ctx, db.NewHangarOutputRepository(consumer))
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())

		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "aborted-executions"}, atc.Config{
			Template: true,
			Jobs: atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.TaskStep{
				Name: "task", Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}},
			}}}}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory = db.NewPipelineRunFactory(dbConn, lockFactory)
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		creation, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{ActivationEpoch: 1, HangarEpoch: 1})
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())
		Expect(creation.EntryBuilds).To(HaveLen(1))
		build = creation.EntryBuilds[0]

		public, private, err := ed25519.GenerateKey(rand.Reader)
		Expect(err).NotTo(HaveOccurred())
		signer, err = executioncontrol.NewAcknowledgementSigner(private)
		Expect(err).NotTo(HaveOccurred())
		verifier = hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}}
	})

	It("stays unfinished and asks Run cancellation to close the execution", func() {
		admitStarted()
		Expect(build.MarkAsAborted()).To(Succeed())
		requested, _ := cancellationRequested()
		Expect(requested).To(BeFalse(), "aborting a build cancelled its Run before anything was stranded")

		Expect(build.Finish(db.BuildStatusAborted)).To(MatchError(atc.ErrRunOutputPending))
		requested, by := cancellationRequested()
		Expect(requested).To(BeTrue(), "an aborted build's unclosed execution has nothing left to close it")
		Expect(by).To(Equal(db.AbortedBuildCancellationRequester))

		var completed bool
		Expect(dbConn.QueryRow(`SELECT completed FROM builds WHERE id=$1`, build.ID()).Scan(&completed)).To(Succeed())
		Expect(completed).To(BeFalse(), "the build finished over an unclosed execution")
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
