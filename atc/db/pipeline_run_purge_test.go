package db_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/event"
	"github.com/concourse/concourse/atc/runinput"
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
	template db.Pipeline
	runID    int
	checkID  int
	taskID   int
	inputRef output.ClaimID
	upload   output.ReservationID
}

func runEvidenceDigest(value string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

// admitRunEvidence creates a team and admits one Run with every kind of
// evidence on its template "evidence".
func admitRunEvidence(ctx context.Context, teamName string) admittedRunEvidence {
	GinkgoHelper()
	team, err := teamFactory.CreateTeam(atc.Team{Name: teamName})
	Expect(err).NotTo(HaveOccurred())
	return admitTemplateRunEvidence(ctx, team, "evidence", 145)
}

// admitTemplateRunEvidence drives one v2 Run of a new template on the team
// through the production admission paths until it holds every kind of
// evidence a purge has to remove: an executed and closed check, an executed
// task with a retained capture and a claimed credential handoff, a bound
// input holding a Hangar claim, a discovered and claimed cancellation queue,
// and a build log. The template also holds a reserved input upload. seed
// keeps each fixture's Hangar tree its own.
func admitTemplateRunEvidence(ctx context.Context, team db.Team, templateName string, seed int) admittedRunEvidence {
	GinkgoHelper()
	consumer, err := db.HangarConsumerPrefixHeld("purge-test")
	Expect(err).NotTo(HaveOccurred())
	repository := db.NewHangarOutputRepository(consumer)
	hangarActivateEpoch(ctx, repository)

	task := &atc.TaskStep{
		Name: "produce", TaskID: uuid.NewString(), RunResult: &atc.RunResult{Name: "result", Output: "result"},
		Config: &atc.TaskConfig{Platform: "linux", Run: atc.TaskRunConfig{Path: "true"}, Outputs: []atc.TaskOutputConfig{{Name: "result"}}},
	}
	template, _, err := team.SavePipeline(atc.PipelineRef{Name: templateName}, atc.Config{
		Template:  true,
		Jobs:      atc.JobConfigs{{Name: "entry", PlanSequence: []atc.Step{{Config: &atc.GetStep{Name: "source"}}, {Config: task}}}},
		Resources: atc.ResourceConfigs{{Name: "source", Type: "some-base-resource-type", Source: atc.Source{"repository": team.Name() + "/" + templateName}}},
	}, 0, false)
	Expect(err).NotTo(HaveOccurred())
	factory := db.NewPipelineRunFactory(dbConn, lockFactory)
	principal := runEvidenceDigest("principal")
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	creation, err := factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{
		HangarOutput: true,
		Invocation:   &db.RunInvocationIdentity{PrincipalDigest: principal, KeyDigest: runEvidenceDigest("key")},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
	run := creation.Run

	witness := func(tx db.Tx, admission db.RunExecutionAdmission, finish bool) {
		GinkgoHelper()
		start := executioncontrol.Acknowledgement{
			ProtocolVersion: executioncontrol.ProtocolVersion, Kind: executioncontrol.AcknowledgementStart,
			Identity: admission.Identity, LedgerSequence: 1,
			NodeUID: "node-uid", PodUID: "pod-uid", ProcessIdentity: "process",
			ObservedAt: output.NewTimestamp(time.Now()),
		}
		Expect(factory.RecordRunExecutionWitness(ctx, tx, admission.BuildID, admission.PlanID, start)).To(Succeed())
		if !finish {
			return
		}
		done := start
		done.Kind = executioncontrol.AcknowledgementFinish
		done.LedgerSequence++
		done.Outcome = &executioncontrol.ExitOutcome{ExitCode: 0}
		Expect(factory.RecordRunExecutionWitness(ctx, tx, admission.BuildID, admission.PlanID, done)).To(Succeed())
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
		NodeName: "node", NodeUID: "node-uid",
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
	record, err := factory.StartRunCapture(ctx, tx, build.ID(), atc.TaskPlan{Name: task.Name, TaskID: task.TaskID, RunResult: task.RunResult, Config: task.Config}, output.DefaultCaptureDeadline, "node", "node-uid")
	Expect(err).NotTo(HaveOccurred())
	admission, owned, err = factory.AdmitRunExecution(ctx, tx, db.RunExecutionRequest{
		BuildID: build.ID(), PlanID: "task-plan", Kind: db.ContainerTypeTask,
		NodeName: "node", NodeUID: "node-uid", Capture: record.Key(),
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(owned).To(BeTrue())
	witness(tx, admission, false)
	Expect(tx.Commit()).To(Succeed())
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	target, err := db.LoadRunCredentialTarget(ctx, tx, template.ID(), run.Number(), principal, "result", true)
	Expect(err).NotTo(HaveOccurred())
	Expect(target.ClaimedNow).To(BeTrue())
	Expect(tx.Commit()).To(Succeed())

	// A bound input holding its own Hangar claim for the life of the Run.
	_, ref := hangarPublish(ctx, repository, hangarDigest(seed), 1725830823000000+int64(seed))
	claim := output.ClaimID(uuid.NewString())
	tx, err = dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	Expect(hangarAcquireClaim(ctx, repository, tx, claim, ref, string(claim))).To(Succeed())
	_, err = tx.Exec(`INSERT INTO pipeline_run_inputs(run_id,name,source_id,scope,digest,generation,claim_id)
		VALUES ($1,'input',$2,$3,$4,$5,$6)`, run.ID(), "input-v1-"+runEvidenceDigest("input"), string(ref.Scope), string(ref.Digest), ref.Generation, string(claim))
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

	// A build log in the team's partition.
	Expect(build.SaveEvent(event.Log{Payload: "evidence"})).To(Succeed())

	// An input upload reserved for the template, not yet any Run's.
	now := time.Now().UTC()
	stage := output.InputStage{
		Version:       output.InputPublicationVersion,
		ReservationID: output.ReservationID(uuid.NewString()),
		NodeUID:       executioncontrol.NodeUID("node-uid"),
		Scope:         ref.Scope,
		Digest:        hangarDigest(seed + 1),
		Bytes:         1024,
		CreatedAt:     output.NewTimestamp(now),
		ExpiresAt:     output.NewTimestamp(now.Add(90 * time.Second)),
	}
	hangarCaptureTx(func(tx db.Tx) {
		Expect(factory.ReserveRunInputUpload(ctx, tx, runinput.Audience{
			TeamID: team.ID(), TemplateID: template.ID(), PrincipalDigest: principal, Input: "upload",
		}, stage, uuid.NewString(), runinput.MaxGrantTTL)).To(Succeed())
	})

	for table, query := range runEvidenceTables {
		var count int
		Expect(dbConn.QueryRow(query, run.ID()).Scan(&count)).To(Succeed())
		Expect(count).To(BeNumerically(">", 0), "the fixture must populate %s", table)
	}
	Expect(runLogCount(team, run.ID())).To(BeNumerically(">", 0), "the fixture must log a build")
	return admittedRunEvidence{team: team, template: template, runID: run.ID(), checkID: check.ID(), taskID: build.ID(), inputRef: claim, upload: stage.ReservationID}
}

// runLogCount counts the build events the Run's builds logged to the team's
// partition.
func runLogCount(team db.Team, runID int) int {
	GinkgoHelper()
	var count int
	Expect(dbConn.QueryRow(fmt.Sprintf(`SELECT count(*) FROM team_build_events_%d
		WHERE build_id IN (SELECT id FROM builds WHERE pipeline_run_id=$1)`, team.ID()), runID).Scan(&count)).To(Succeed())
	return count
}

// runEvidenceCounts counts each kind of the Run's evidence.
func runEvidenceCounts(runID int) map[string]int {
	GinkgoHelper()
	counts := map[string]int{}
	for table, query := range runEvidenceTables {
		var count int
		Expect(dbConn.QueryRow(query, runID).Scan(&count)).To(Succeed())
		counts[table] = count
	}
	return counts
}

func claimReleased(claim output.ClaimID) bool {
	GinkgoHelper()
	var released bool
	Expect(dbConn.QueryRow(`SELECT released_at IS NOT NULL FROM hangar_claims WHERE claim_id=$1`, string(claim)).Scan(&released)).To(Succeed())
	return released
}

func uploadRetained(id output.ReservationID) bool {
	GinkgoHelper()
	var retained bool
	Expect(dbConn.QueryRow(`SELECT EXISTS(SELECT 1 FROM pipeline_run_input_uploads WHERE reservation_id=$1)`, string(id)).Scan(&retained)).To(Succeed())
	return retained
}

// settleRunAborted stands the fixture's Run where the cancellation worker
// would eventually leave it: aborted, with its cancellation work done. The
// fixture's executions have no node to stop, so the worker cannot get it
// there itself; the terminal publication and the queue rows are immutable by
// trigger, so the fixture steps around them for these rows only. heldTerm,
// when positive, leaves the claimed operation held by the worker for that long
// instead of done: the aborted publication may commit before the worker records
// its terminal operation.
func settleRunAborted(runID int, heldTerm time.Duration) {
	GinkgoHelper()
	tx, err := dbConn.Begin()
	Expect(err).NotTo(HaveOccurred())
	defer db.Rollback(tx)
	_, err = tx.Exec(`SET LOCAL session_replication_role = replica`)
	Expect(err).NotTo(HaveOccurred())
	_, err = tx.Exec(`UPDATE pipeline_runs SET status='aborted', completed_at=now(), result_manifest='{}' WHERE id=$1`, runID)
	Expect(err).NotTo(HaveOccurred())
	if heldTerm > 0 {
		_, err = tx.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=clock_timestamp()+$2::interval WHERE run_id=$1 AND claimed`, runID, fmt.Sprintf("%d seconds", int(heldTerm.Seconds())))
	} else {
		_, err = tx.Exec(`UPDATE pipeline_run_cancellation_operations SET claimed=false, debt='', completed_at=clock_timestamp() WHERE run_id=$1 AND completed_at IS NULL`, runID)
	}
	Expect(err).NotTo(HaveOccurred())
	Expect(tx.Commit()).To(Succeed())
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
		Expect(claimReleased(evidence.inputRef)).To(BeTrue(), "a purged Run must not pin a Hangar tree forever; the claim tombstone remains")
		Expect(uploadRetained(evidence.upload)).To(BeFalse(), "team purge must remove the team's input uploads")
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

var _ = Describe("Destroying a template with its Run history", func() {
	var (
		ctx                 context.Context
		purged, sibling     admittedRunEvidence
		otherTeam           admittedRunEvidence
		siblingEvidence     map[string]int
		otherTeamEvidence   map[string]int
		siblingLogs         int
		otherTeamLogs       int
		purgedTemplateRunID int
	)

	BeforeEach(func() {
		ctx = context.Background()
		purged = admitRunEvidence(ctx, "template-purge")
		purgedTemplateRunID = purged.runID
		sibling = admitTemplateRunEvidence(ctx, purged.team, "sibling", 147)
		otherTeam = admitRunEvidence(ctx, "template-purge-other")
		siblingEvidence = runEvidenceCounts(sibling.runID)
		otherTeamEvidence = runEvidenceCounts(otherTeam.runID)
		siblingLogs = runLogCount(sibling.team, sibling.runID)
		otherTeamLogs = runLogCount(otherTeam.team, otherTeam.runID)
	})

	untouched := func() {
		GinkgoHelper()
		Expect(runEvidenceCounts(sibling.runID)).To(Equal(siblingEvidence), "another template's Runs on the same team must be untouched")
		Expect(runEvidenceCounts(otherTeam.runID)).To(Equal(otherTeamEvidence), "another team's Runs must be untouched")
		Expect(runLogCount(sibling.team, sibling.runID)).To(Equal(siblingLogs))
		Expect(runLogCount(otherTeam.team, otherTeam.runID)).To(Equal(otherTeamLogs))
		for _, kept := range []admittedRunEvidence{sibling, otherTeam} {
			Expect(claimReleased(kept.inputRef)).To(BeFalse(), "another template's claims must stay held")
			Expect(uploadRetained(kept.upload)).To(BeTrue(), "another template's input uploads must stay")
			_, found, err := kept.team.Pipeline(atc.PipelineRef{Name: kept.template.Name()})
			Expect(err).NotTo(HaveOccurred())
			Expect(found).To(BeTrue())
		}
	}

	retained := func(evidence admittedRunEvidence) {
		GinkgoHelper()
		for table, count := range runEvidenceCounts(evidence.runID) {
			Expect(count).To(BeNumerically(">", 0), "a refused purge must keep %s", table)
		}
		Expect(claimReleased(evidence.inputRef)).To(BeFalse())
		Expect(uploadRetained(evidence.upload)).To(BeTrue())
		_, found, err := evidence.team.Pipeline(atc.PipelineRef{Name: evidence.template.Name()})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "a refused purge must keep the template")
	}

	It("still refuses a plain destroy", func() {
		settleRunAborted(purged.runID, 0)
		Expect(purged.template.Destroy()).To(MatchError(db.ErrPipelineTemplateHasRunHistory))
		retained(purged)
	})

	It("removes exactly the template's Runs and their evidence, releases their claims, and the template goes", func() {
		settleRunAborted(purged.runID, 0)

		count, err := purged.template.DestroyWithRunHistory()
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(1))

		for table, n := range runEvidenceCounts(purgedTemplateRunID) {
			Expect(n).To(BeZero(), "the template purge must remove %s", table)
		}
		Expect(runLogCount(purged.team, purgedTemplateRunID)).To(BeZero(), "the template purge must remove its Runs' build logs")
		Expect(claimReleased(purged.inputRef)).To(BeTrue(), "a purged Run must not pin a Hangar tree forever; the claim tombstone remains")
		Expect(uploadRetained(purged.upload)).To(BeFalse(), "the template purge must remove the template's input uploads")
		var leftovers int
		Expect(dbConn.QueryRow(`SELECT
			(SELECT count(*) FROM pipelines WHERE id=$1) +
			(SELECT count(*) FROM pipelines WHERE pipeline_run_id=$2) +
			(SELECT count(*) FROM task_caches WHERE template_pipeline_id=$1)`, purged.template.ID(), purgedTemplateRunID).Scan(&leftovers)).To(Succeed())
		Expect(leftovers).To(BeZero(), "the template, its payloads and its task caches must go")
		_, found, err := purged.team.Pipeline(atc.PipelineRef{Name: purged.template.Name()})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		untouched()

		// The marker was transaction-scoped: nothing after the purge may
		// delete the surviving Runs' evidence.
		_, err = dbConn.Exec(`DELETE FROM pipeline_run_inputs WHERE run_id=$1`, sibling.runID)
		Expect(err).To(HaveOccurred())
	})

	It("refuses while a Run is still running, and deletes nothing", func() {
		count, err := purged.template.DestroyWithRunHistory()
		Expect(err).To(MatchError(db.ErrPipelineTemplateHasActiveRun))
		Expect(count).To(BeZero())
		retained(purged)
		untouched()
	})

	It("refuses while the cancellation worker still holds an aborted Run's work, until its term runs out", func() {
		settleRunAborted(purged.runID, time.Hour)
		_, err := purged.template.DestroyWithRunHistory()
		Expect(err).To(MatchError(db.ErrPipelineTemplateHasActiveRun))
		retained(purged)

		// Past its term no worker can complete the operation on a Run that
		// has left running, so it no longer holds the Run active.
		tx, err := dbConn.Begin()
		Expect(err).NotTo(HaveOccurred())
		defer db.Rollback(tx)
		_, err = tx.Exec(`SET LOCAL session_replication_role = replica`)
		Expect(err).NotTo(HaveOccurred())
		_, err = tx.Exec(`UPDATE pipeline_run_cancellation_operations SET next_at=clock_timestamp()-interval '1 second' WHERE run_id=$1 AND claimed`, purged.runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(tx.Commit()).To(Succeed())

		count, err := purged.template.DestroyWithRunHistory()
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(Equal(1))
		untouched()
	})

	It("destroys a template without Runs as a plain destroy does, purging none", func() {
		template, _, err := purged.team.SavePipeline(atc.PipelineRef{Name: "never-run"}, atc.Config{
			Template: true, Jobs: atc.JobConfigs{{Name: "entry"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		count, err := template.DestroyWithRunHistory()
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(BeZero())
		_, found, err := purged.team.Pipeline(atc.PipelineRef{Name: "never-run"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
		untouched()
	})

	It("destroys an ordinary pipeline as a plain destroy does, purging none", func() {
		pipeline, _, err := purged.team.SavePipeline(atc.PipelineRef{Name: "ordinary"}, atc.Config{
			Jobs: atc.JobConfigs{{Name: "job"}},
		}, 0, false)
		Expect(err).NotTo(HaveOccurred())
		count, err := pipeline.DestroyWithRunHistory()
		Expect(err).NotTo(HaveOccurred())
		Expect(count).To(BeZero())
		_, found, err := purged.team.Pipeline(atc.PipelineRef{Name: "ordinary"})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
	})

	Context("when a landing queue used the template's Runs", func() {
		var queueID int

		BeforeEach(func() {
			settleRunAborted(purged.runID, 0)
			Expect(dbConn.QueryRow(`INSERT INTO landing_queues(team_id,name,config) VALUES ($1,'trunk','{}') RETURNING id`, purged.team.ID()).Scan(&queueID)).To(Succeed())
		})

		entry := func(entryID string, settled bool, composeRunID, landRunID any) int {
			GinkgoHelper()
			state, settledAt := "in_flight", any(nil)
			if settled {
				state, settledAt = "landed", time.Now()
			}
			var id int
			Expect(dbConn.QueryRow(`INSERT INTO landing_entries(queue_id,entry_id,commit,state,settled_at,compose_run_id,land_run_id)
				VALUES ($1,$2,'abc123',$3,$4,$5,$6) RETURNING id`, queueID, entryID, state, settledAt, composeRunID, landRunID).Scan(&id)).To(Succeed())
			return id
		}
		intent := func(entryRowID int, composeRunID, landRunID any) {
			GinkgoHelper()
			_, err := dbConn.Exec(`INSERT INTO landing_intents(queue_id,entry_id,compose_run_id,land_run_id) VALUES ($1,$2,$3,$4)`, queueID, entryRowID, composeRunID, landRunID)
			Expect(err).NotTo(HaveOccurred())
		}
		refuses := func() {
			GinkgoHelper()
			count, err := purged.template.DestroyWithRunHistory()
			Expect(err).To(MatchError(db.ErrPipelineTemplateHasPendingLanding))
			Expect(count).To(BeZero())
			retained(purged)
			untouched()
		}

		It("refuses while an unsettled entry names one of its Runs", func() {
			entry("in-flight", false, nil, purged.runID)
			refuses()
		})

		It("refuses while an unsettled entry owns an intent naming one of its Runs", func() {
			id := entry("in-flight", false, nil, nil)
			intent(id, purged.runID, nil)
			refuses()
		})

		It("purges once the entry has settled, which keeps its outcome but loses the link to the Runs", func() {
			id := entry("landed", true, sibling.runID, purged.runID)
			intent(id, purged.runID, nil)

			count, err := purged.template.DestroyWithRunHistory()
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(1))

			var state string
			var compose, land sql.NullInt64
			Expect(dbConn.QueryRow(`SELECT state, compose_run_id, land_run_id FROM landing_entries WHERE id=$1`, id).Scan(&state, &compose, &land)).To(Succeed())
			Expect(state).To(Equal("landed"))
			Expect(compose.Int64).To(BeEquivalentTo(sibling.runID), "another template's Run stays linked")
			Expect(land.Valid).To(BeFalse())
			var intents int
			Expect(dbConn.QueryRow(`SELECT count(*) FROM landing_intents WHERE entry_id=$1`, id).Scan(&intents)).To(Succeed())
			Expect(intents).To(BeZero())
			untouched()
		})

		It("ignores unsettled entries that name only another template's Runs", func() {
			id := entry("in-flight", false, sibling.runID, nil)
			intent(id, sibling.runID, nil)
			count, err := purged.template.DestroyWithRunHistory()
			Expect(err).NotTo(HaveOccurred())
			Expect(count).To(Equal(1))
			untouched()
		})
	})

	It("refuses a Run's payload as a plain destroy does", func() {
		payload, found, err := db.NewPipelineRunFactory(dbConn, lockFactory).InstancePipeline(mustRun(sibling.runID))
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		_, err = payload.DestroyWithRunHistory()
		Expect(err).To(MatchError(db.ErrPipelineRunPayloadMutation))
		untouched()
	})
})

func mustRun(id int) db.PipelineRun {
	GinkgoHelper()
	run, found, err := db.NewPipelineRunFactory(dbConn, lockFactory).GetRunByID(id)
	Expect(err).NotTo(HaveOccurred())
	Expect(found).To(BeTrue())
	return run
}

var _ = Describe("A cancelled Run's header after payload reclamation", func() {
	It("keeps the request, the empty aborted publication, the definition, the input claims, and cancellation itself deleted nothing", func() {
		ctx := context.Background()
		consumer, err := db.HangarConsumerPrefixHeld("cancelled-header-test")
		Expect(err).NotTo(HaveOccurred())
		repository := db.NewHangarOutputRepository(consumer)
		hangarActivateEpoch(ctx, repository)

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
		create := func(key string) db.PipelineRun {
			GinkgoHelper()
			var creation db.RunCreation
			inTx(func(tx db.Tx) error {
				var err error
				creation, err = factory.CreateRunInTx(ctx, tx, template, db.RunParams{}, "creator", db.RunCreationOpts{
					HangarOutput: true,
					Invocation:   &db.RunInvocationIdentity{PrincipalDigest: runEvidenceDigest("principal"), KeyDigest: runEvidenceDigest(key)},
				})
				return err
			})
			return creation.Run
		}

		run := create("cancelled")
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
			_, err := tx.Exec(`INSERT INTO pipeline_run_inputs(run_id,name,source_id,scope,digest,generation,claim_id)
				VALUES ($1,'input',$2,$3,$4,$5,$6)`, run.ID(), "input-v1-"+runEvidenceDigest("cancelled-input"), string(ref.Scope), string(ref.Digest), ref.Generation, string(claim))
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
		Expect(published.CompletedAt).NotTo(BeZero())

		// Cancellation itself deleted nothing: the payload and the input claim
		// outlive the aborted publication.
		aborted, _, err := factory.GetRunByID(run.ID())
		Expect(err).NotTo(HaveOccurred())
		_, found, err = factory.InstancePipeline(aborted)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue(), "cancellation deleted the Run's payload")
		Expect(inputClaimHeld()).To(BeTrue(), "cancellation released the Run's input claim")

		// A later Run makes the cancelled one fall outside keep_last.
		create("successor")
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
		Expect(header.Status()).To(Equal(atc.RunStatusAborted))
		Expect(header.CompletedAt()).NotTo(BeNil())
		Expect(*header.CompletedAt()).To(BeTemporally("==", published.CompletedAt))
		Expect(header.ConfigHash()).To(Equal(run.ConfigHash()))

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
