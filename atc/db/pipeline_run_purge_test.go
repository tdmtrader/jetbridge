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
	"pipeline_run_output_starts":            `SELECT count(*) FROM pipeline_run_output_starts WHERE run_id=$1`,
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
// executed and closed check, an executed task with a retained output start and
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

	// An executed task with an output start and a claimed credential handoff.
	build := creation.EntryBuilds[0]
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	record, err := factory.PredeclareOutputTask(ctx, tx, build.ID(), atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}, 1, output.DefaultCaptureDeadline, "node", "node-uid")
	Expect(err).NotTo(HaveOccurred())
	admission, owned, err = factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
		BuildID: build.ID(), PlanID: "task-plan", Kind: db.ContainerTypeTask,
		Epoch: 1, NodeName: "node", NodeUID: "node-uid", HandoffID: record.HandoffID,
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
	Expect(repository.AcquireClaim(ctx, tx, output.ClaimAcquisition{
		ProtocolVersion: output.ProtocolVersion, ClaimID: claim, Ref: ref,
		ConsumerBindingID: output.OpaqueID(claim), RequestedAt: output.NewTimestamp(time.Now()),
	})).To(Succeed())
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
			"pipeline_run_output_starts":           `DELETE FROM pipeline_run_output_starts WHERE run_id=$1`,
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
