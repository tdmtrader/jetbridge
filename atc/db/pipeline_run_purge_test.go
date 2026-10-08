package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/hangaroutput"
	"github.com/concourse/concourse/hangar/executioncontrol"
	"github.com/concourse/concourse/hangar/output"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// runEvidenceTables lists every table that retains Run evidence keyed, directly
// or through an execution or handoff, by one Run. Each query counts that Run's
// rows.
var runEvidenceTables = map[string]string{
	"pipeline_run_executions":               `SELECT count(*) FROM pipeline_run_executions WHERE run_id=$1`,
	"pipeline_run_execution_starts":         `SELECT count(*) FROM pipeline_run_execution_starts s JOIN pipeline_run_executions e USING(execution_id,execution_fence) WHERE e.run_id=$1`,
	"pipeline_run_execution_closures":       `SELECT count(*) FROM pipeline_run_execution_closures c JOIN pipeline_run_executions e USING(execution_id,execution_fence) WHERE e.run_id=$1`,
	"pipeline_run_captures":                 `SELECT count(*) FROM pipeline_run_captures WHERE run_id=$1`,
	"pipeline_run_credential_handoffs":      `SELECT count(*) FROM pipeline_run_credential_handoffs WHERE run_id=$1`,
	"pipeline_run_cancellation_progress":    `SELECT count(*) FROM pipeline_run_cancellation_progress WHERE run_id=$1`,
	"pipeline_run_cancellation_operations":  `SELECT count(*) FROM pipeline_run_cancellation_operations WHERE run_id=$1`,
	"pipeline_run_cancellation_cursors":     `SELECT count(*) FROM pipeline_run_cancellation_cursors WHERE run_id=$1`,
	"pipeline_run_inputs":                   `SELECT count(*) FROM pipeline_run_inputs WHERE run_id=$1`,
	"pipeline_run_invocations":              `SELECT count(*) FROM pipeline_run_invocations WHERE run_id=$1`,
	"pipeline_run_definitions":              `SELECT count(*) FROM pipeline_run_definitions WHERE run_id=$1`,
	"builds of the Run (job and check)":     `SELECT count(*) FROM builds WHERE pipeline_run_id=$1`,
	"pipeline_runs (the Run header itself)": `SELECT count(*) FROM pipeline_runs WHERE id=$1`,
}

type admittedRunEvidence struct {
	team     db.Team
	runID    int
	checkID  int
	taskID   int
	inputRef output.ClaimID
}

func runEvidenceDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

// admitRunEvidence drives one v2 Run through the production admission paths
// until it holds every kind of evidence a team purge has to remove: an
// executed and closed check, an executed task with a retained capture and
// a claimed credential handoff, a bound input holding a Hangar claim, and a
// discovered and claimed cancellation queue.
func admitRunEvidence(ctx context.Context, teamName string) admittedRunEvidence {
	GinkgoHelper()
	consumer, err := db.HangarConsumerPrefixHeld("purge-test")
	Expect(err).NotTo(HaveOccurred())
	repository := db.NewHangarOutputRepository(consumer)
	hangarActivateEpoch(ctx, repository)
	_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
	Expect(err).NotTo(HaveOccurred())

	team, err := teamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	task := &atc.TaskStep{
		Name: "produce", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "result", Output: "result"},
		Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
	}
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: "evidence"}, atc.Config{
		Template:  true,
		Jobs:      atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}, {Config: task}}}},
		Resources: atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": "example"}}},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	factory := db.NewPipelineRunFactory(dbConn, lockFactory)
	principal := runEvidenceDigest("principal")
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{
		ActivationEpoch: 1,
		HangarEpoch:     1,
		Invocation:      &db.RunInvocationIdentity{PrincipalDigest: principal, KeyDigest: runEvidenceDigest("key")},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	run := creation.Run

	public, private, err := ed25519.GenerateKey(rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	signer, err := executioncontrol.NewAcknowledgementSigner(private)
	Expect(err).NotTo(HaveOccurred())
	verifier := hangaroutput.ControlKeyRing{ActivationEpoch: 1, Keys: []hangaroutput.ControlKeyEntry{{Epoch: 1, PublicKey: base64.StdEncoding.EncodeToString(public)}}}
	witness := func(tx db.Tx, admission db.RunExecutionAdmission, finish bool) {
		GinkgoHelper()
		start, err := signer.Sign(executioncontrol.Acknowledgement{
			ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementStart,
			Identity: admission.Identity, ActivationEpoch: 1, LedgerSequence: 1,
			NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: "process",
			ObservedAt: output.NewTimestamp(time.Now()),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordRunExecutionWitness(ctx, tx, admission.BuildID, admission.PlanID, start, verifier)).To(Succeed())
		if !finish {
			return
		}
		done := start
		done.Kind = executioncontrol.AcknowledgementFinish
		done.LedgerSequence++
		done.Outcome = &executioncontrol.ExitOutcome{ExitCode: 0}
		done, err = signer.Sign(done)
		Expect(err).NotTo(HaveOccurred())
		Expect(factory.RecordRunExecutionWitness(ctx, tx, admission.BuildID, admission.PlanID, done, verifier)).To(Succeed())
	}

	// An executed, closed and finished check.
	payload, found, err := factory.InstancePipeline(run)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	resource, found, err := payload.Resource("source")
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	check, created, err := checkFactory.TryCreateCheck(ctx, resource, db.ResourceTypes{}, nil, true, false, false)
	Expect(err).NotTo(HaveOccurred())
	Expect(created).To(BeTrue())
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	admission, owned, err := factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
		BuildID: check.ID(), PlanID: check.PrivatePlan().ID, Kind: db.ContainerTypeCheck,
		Epoch: 1, NodeName: "node", NodeUID: "node-uid",
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(owned).To(BeTrue())
	witness(tx, admission, true)
	Expect(tx.Commit()).To(Succeed())
	Expect(check.Finish(db.BuildStatusSucceeded)).To(Succeed())

	// An executed task with a capture and a claimed credential handoff.
	build := creation.EntryBuilds[0]
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	record, err := factory.StartRunCapture(ctx, tx, build.ID(), atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}, 1, output.DefaultCaptureDeadline, "node", "node-uid")
	Expect(err).NotTo(HaveOccurred())
	admission, owned, err = factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
		BuildID: build.ID(), PlanID: "task-plan", Kind: db.ContainerTypeTask,
		Epoch: 1, NodeName: "node", NodeUID: "node-uid", Capture: record.Key(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(owned).To(BeTrue())
	witness(tx, admission, false)
	Expect(tx.Commit()).To(Succeed())
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	target, err := db.LoadRunCredentialTarget(ctx, tx, template.ID(), run.Number(), principal, "result", 1, true)
	Expect(err).NotTo(HaveOccurred())
	Expect(target.ClaimedNow).To(BeTrue())
	Expect(tx.Commit()).To(Succeed())

	// A bound input holding its own Hangar claim for the life of the Run.
	_, ref := hangarPublish(ctx, repository, hangarDigest(145), 1725830823000145)
	claim := output.ClaimID(uuid.NewString())
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(hangarAcquireClaim(ctx, repository, tx, claim, ref, string(claim))).To(Succeed())
	_, err = tx.Exec(`INSERT INTO pipeline_run_inputs(run_id,name,source_id,scope,digest,generation,claim_id,activation_epoch)
		VALUES ($1,'input',$2,$3,$4,$5,$6,1)`, run.ID(), "input-v1-"+runEvidenceDigest("input"), string(ref.Scope), string(ref.Digest), ref.Generation, string(claim))
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())

	// A discovered and claimed cancellation queue.
	_, err = factory.RequestRunCancellation(ctx, run.ID(), "canceller", nil)
	Expect(err).NotTo(HaveOccurred())
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	lease, leased, err := factory.ClaimRunCancellationLease(ctx, tx, "purge-test", time.Minute)
	Expect(err).NotTo(HaveOccurred())
	Expect(leased).To(BeTrue())
	_, err = factory.DiscoverRunCancellation(ctx, tx, lease, run.ID(), 100)
	Expect(err).NotTo(HaveOccurred())
	_, claimed, err := factory.ClaimRunCancellationOperation(ctx, tx, lease, run.ID())
	Expect(err).NotTo(HaveOccurred())
	Expect(claimed).To(BeTrue())
	Expect(tx.Commit()).To(Succeed())

	for table, query := range runEvidenceTables {
		var count int
		Expect(dbConn.QueryRow(query, run.ID()).Scan(&count)).To(Succeed())
		Expect(count).To(BeNumerically(">", 0), "the fixture must populate %s", table)
	}
	return admittedRunEvidence{team: team, runID: run.ID(), checkID: check.ID(), taskID: build.ID(), inputRef: claim}
}

var _ = Describe("Run evidence and team purge", func() {
	It("removes every kind of Run evidence and releases the Run's Hangar claims", func() {
		ctx := context.Background()
		evidence := admitRunEvidence(ctx, "evidence-purge")

		Expect(evidence.team.Delete()).To(Succeed())

		_, found, err := teamFactory.FindTeam("evidence-purge")
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		for table, query := range runEvidenceTables {
			var count int
			Expect(dbConn.QueryRow(query, evidence.runID).Scan(&count)).To(Succeed())
			Expect(count).To(BeZero(), "team purge must remove %s", table)
		}
		var released bool
		Expect(dbConn.QueryRow(`SELECT released_at IS NOT NULL FROM hangar_claims WHERE claim_id=$1`, string(evidence.inputRef)).Scan(&released)).To(Succeed())
		Expect(released).To(BeTrue(), "a purged Run must not pin a Hangar tree forever; the claim tombstone remains")
	})

	It("still refuses to delete Run evidence outside a team purge", func() {
		ctx := context.Background()
		evidence := admitRunEvidence(ctx, "evidence-retained")

		refusals := map[string]string{
			"pipeline_run_executions":              `DELETE FROM pipeline_run_executions WHERE run_id=$1`,
			"pipeline_run_execution_starts":        `DELETE FROM pipeline_run_execution_starts WHERE (execution_id,execution_fence) IN (SELECT execution_id,execution_fence FROM pipeline_run_executions WHERE run_id=$1)`,
			"pipeline_run_execution_closures":      `DELETE FROM pipeline_run_execution_closures WHERE (execution_id,execution_fence) IN (SELECT execution_id,execution_fence FROM pipeline_run_executions WHERE run_id=$1)`,
			"pipeline_run_credential_handoffs":     `DELETE FROM pipeline_run_credential_handoffs WHERE run_id=$1`,
			"pipeline_run_cancellation_operations": `DELETE FROM pipeline_run_cancellation_operations WHERE run_id=$1`,
			"pipeline_run_inputs":                  `DELETE FROM pipeline_run_inputs WHERE run_id=$1`,
			"pipeline_run_captures":                `DELETE FROM pipeline_run_captures WHERE run_id=$1`,
			"pipeline_run_invocations":             `DELETE FROM pipeline_run_invocations WHERE run_id=$1`,
			"pipeline_run_definitions":             `DELETE FROM pipeline_run_definitions WHERE run_id=$1`,
		}
		for table, statement := range refusals {
			// Row triggers fire before referential checks, so the refusal must
			// be the evidence guard itself, never a foreign key that happens to
			// protect the row today.
			_, err := dbConn.Exec(statement, evidence.runID)
			Expect(err).To(HaveOccurred(), "%s must stay undeletable outside a team purge", table)
			Expect(err.Error()).NotTo(ContainSubstring("foreign key"), table)
		}
		for _, buildID := range []int{evidence.checkID, evidence.taskID} {
			_, err := dbConn.Exec(`DELETE FROM builds WHERE id=$1`, buildID)
			Expect(err).To(HaveOccurred(), "an executed build must not be deletable while its evidence exists")
		}
		_, err := dbConn.Exec(`UPDATE pipeline_run_executions SET node_name='elsewhere' WHERE run_id=$1`, evidence.runID)
		Expect(err).To(MatchError(ContainSubstring("immutable")))

		// The purge marker is transaction-scoped: a purge in one transaction
		// grants nothing to the next.
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`SELECT set_config('concourse.pipeline_run_team_purge', 'on', true)`)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Rollback()).To(Succeed())
		_, err = dbConn.Exec(refusals["pipeline_run_inputs"], evidence.runID)
		Expect(err).To(HaveOccurred())

		for table, query := range runEvidenceTables {
			var count int
			Expect(dbConn.QueryRow(query, evidence.runID).Scan(&count)).To(Succeed())
			Expect(count).To(BeNumerically(">", 0), "%s must be retained", table)
		}
	})
})

var _ = Describe("A cancelled Run's header after payload reclamation", func() {
	It("keeps the request, the empty aborted publication, the definition, the input claims and the cause, and cancellation itself deleted nothing", func() {
		ctx := context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("cancelled-header-test")
		Expect(err).NotTo(HaveOccurred())
		repository := db.NewHangarOutputRepository(consumer)
		hangarActivateEpoch(ctx, repository)
		_, err = dbConn.Exec(`UPDATE pipeline_run_activation SET epoch=1, admission_enabled=true WHERE singleton`)
		Expect(err).NotTo(HaveOccurred())

		keepLast := 1
		template, _, err := defaultTeam.SavePipeline(atc.PipelineRef{Name: "cancelled-header"}, atc.Config{
			Template: true, RunRetention: &atc.RunRetentionConfig{KeepLast: &keepLast}, Jobs: atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		factory := db.NewPipelineRunFactory(dbConn, lockFactory)
		inTx := func(fn func(db.Tx) error) {
			GinkgoHelper()
			tx, err := dbConn.Begin()
			Expect(err).NotTo(HaveOccurred())
			defer db.Rollback(tx)
			Expect(fn(tx)).To(Succeed())
			Expect(tx.Commit()).To(Succeed())
		}
		create := func(key string, cause *int, correlation string) db.PipelineRun {
			GinkgoHelper()
			var creation db.RunCreation
			inTx(func(tx db.Tx) error {
				var err error
				creation, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{
					ActivationEpoch: 1, HangarEpoch: 1,
					Invocation:  &db.RunInvocationIdentity{PrincipalDigest: runEvidenceDigest("principal"), KeyDigest: runEvidenceDigest(key)},
					CausedByRun: cause, Correlation: correlation,
				})
				return err
			})
			return creation.Run
		}

		cause := create("cause", nil, "")
		causeID := cause.ID()
		run := create("cancelled", &causeID, "change-42")
		definition, found, err := factory.Definition(run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		// A bound input holding its own Hangar claim for the life of the header.
		_, ref := hangarPublish(ctx, repository, hangarDigest(146), 1725830823000146)
		claim := output.ClaimID(uuid.NewString())
		inTx(func(tx db.Tx) error {
			if _, err := repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
				ProtocolVersion: output.ProtocolVersion, ClaimID: claim, Ref: ref,
				ConsumerBindingID: output.OpaqueID(claim), RequestedAt: output.NewTimestamp(time.Now()),
			}); err != nil {
				return err
			}
			_, err := tx.Exec(`INSERT INTO pipeline_run_inputs(run_id,name,source_id,scope,digest,generation,claim_id,activation_epoch)
				VALUES ($1,'input',$2,$3,$4,$5,$6,1)`, run.ID(), "input-v1-"+runEvidenceDigest("cancelled-input"), string(ref.Scope), string(ref.Digest), ref.Generation, string(claim))
			return err
		})
		inputClaimHeld := func() bool {
			GinkgoHelper()
			var held bool
			Expect(dbConn.QueryRow(`SELECT count(*)=1 AND bool_and(c.released_at IS NULL) FROM pipeline_run_inputs i
				JOIN hangar_claims c ON c.claim_id=i.claim_id WHERE i.run_id=$1`, run.ID()).Scan(&held)).To(Succeed())
			return held
		}

		reason := "operator stop"
		outcome, err := factory.RequestRunCancellation(ctx, run.ID(), "operator", &reason)
		Expect(err).NotTo(HaveOccurred())
		Expect(outcome).To(Equal(atc.RunCancelAccepted))
		requested, found, err := factory.GetRunByID(run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		request := requested.CancellationRequest()
		Expect(request).NotTo(BeNil())

		// The worker's own operations converge a Run with no execution; a
		// source kind here would need a node and fails the fixture.
		var lease db.RunCancellationLease
		inTx(func(tx db.Tx) error {
			var owned bool
			var err error
			lease, owned, err = factory.ClaimRunCancellationLease(ctx, tx, "cancelled-header-test", time.Minute)
			Expect(owned).To(BeTrue())
			return err
		})
		Eventually(func() atc.RunStatus {
			_, err := dbConn.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=now() - interval '1 second' WHERE completed_at IS NULL AND run_id=$1`, run.ID())
			Expect(err).NotTo(HaveOccurred())
			var op db.RunCancellationOperation
			var claimed bool
			inTx(func(tx db.Tx) error {
				if _, err := factory.DiscoverRunCancellation(ctx, tx, lease, run.ID(), db.RunCancellationOperationLimit); err != nil {
					return err
				}
				var err error
				op, claimed, err = factory.ClaimRunCancellationOperation(ctx, tx, lease, run.ID())
				return err
			})
			if claimed {
				var debt db.RunCancellationDebt
				switch op.Kind {
				case db.CancelSchedulerDebt:
					debt, err = factory.ExecuteCancellationOperation(ctx, lease, op)
				case db.CancelBuild, db.CancelCandidate, db.CancelTerminalize:
					debt, err = factory.ExecuteCancellationFinality(ctx, lease, op)
				default:
					Fail("a Run with no execution discovered " + string(op.Kind))
				}
				Expect(err).NotTo(HaveOccurred())
				inTx(func(tx db.Tx) error { return factory.RecordRunCancellationProgress(ctx, tx, lease, op, debt) })
			}
			reloaded, _, err := factory.GetRunByID(run.ID())
			Expect(err).NotTo(HaveOccurred())
			return reloaded.Status()
		}).WithTimeout(10 * time.Second).Should(Equal(atc.RunStatusAborted))

		published, found, err := factory.TerminalResult(ctx, run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(published.Results).To(Equal(map[string]atc.RunResultBinding{}), "cancellation publishes an explicit empty result map")
		Expect(published.Version).NotTo(BeEmpty())

		// Cancellation itself deleted nothing: the payload and the input claim
		// outlive the aborted publication.
		aborted, _, err := factory.GetRunByID(run.ID())
		Expect(err).NotTo(HaveOccurred())
		_, found, err = factory.InstancePipeline(aborted)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "cancellation deleted the Run's payload")
		Expect(inputClaimHeld()).To(BeTrue(), "cancellation released the Run's input claim")

		// A later Run makes the cancelled one fall outside keep_last.
		create("successor", nil, "")
		destroyed, err := db.NewPipelineRunReclaimLifecycle(dbConn).DestroyReclaimableRun(run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(destroyed).To(BeTrue())

		header, found, err := factory.GetRunByID(run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "reclamation destroyed the Run header")
		_, found, err = factory.InstancePipeline(header)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse(), "the fixture did not reclaim the payload")

		Expect(header.CancellationRequest()).To(Equal(request))
		Expect(header.CancellationRequest().RequestedBy).To(Equal("operator"))
		Expect(header.CancellationRequest().Reason).To(Equal(&reason))
		Expect(header.ContractVersion()).To(Equal(atc.RunContractV2))
		Expect(header.Status()).To(Equal(atc.RunStatusAborted))
		Expect(header.CompletedAt()).NotTo(BeNil())
		Expect(*header.CompletedAt()).To(BeTemporally("==", published.CompletedAt))
		Expect(header.ConfigHash()).To(Equal(run.ConfigHash()))
		Expect(header.CausedByRun()).To(Equal(&causeID))
		Expect(header.Correlation()).To(Equal("change-42"))

		retained, found, err := factory.TerminalResult(ctx, run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(retained).To(Equal(published), "reclamation changed the immutable aborted publication")
		kept, found, err := factory.Definition(run.ID())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "reclamation dropped the Run's definition")
		Expect(kept).To(Equal(definition))
		Expect(inputClaimHeld()).To(BeTrue(), "reclamation released the Run's input claim")
	})
})
