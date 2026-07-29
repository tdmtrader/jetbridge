package db_test

import (
	"errors"
	"strings"
	"sync"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentRunTranscriptFactory", func() {
	var factory db.AgentRunTranscriptFactory

	BeforeEach(func() {
		factory = db.NewAgentRunTranscriptFactory(dbConn)
	})

	// createWorkflowRun materializes a durable workflow run (and the
	// definition it points at) so transcripts can carry the v3 execution
	// identity the ListByWorkflowRun join asserts.
	createWorkflowRun := func(workflowName, idempotencyKey string, buildID int) snapshot.WorkflowRunID {
		GinkgoHelper()
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, workflowName, strings.Repeat("a", 64)).Scan(&definitionID)).To(Succeed())

		var runID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'alice', 'running', $8)
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, workflowName,
			strings.Repeat("a", 64), idempotencyKey, strings.Repeat("b", 64), buildID).Scan(&runID)).To(Succeed())

		return snapshot.WorkflowRunID(runID)
	}

	createAttempt := func(buildID int, planID, functionID string, workflowRunID *snapshot.WorkflowRunID) (int64, int64) {
		GinkgoHelper()
		var headID, attemptID int64
		var workflowRunIDValue any
		if workflowRunID != nil {
			workflowRunIDValue = int64(*workflowRunID)
		}
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_run_checkpoint_heads
				(workflow_run_provenance_id, workflow_run_id, build_id, plan_id, function_id)
			VALUES ($1, $1, $2, $3, $4)
			RETURNING id
		`, workflowRunIDValue, buildID, planID, functionID).Scan(&headID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, state, is_current, materialization_id)
			VALUES ($1, 1, 'scheduling', TRUE, 'transcript-materialization-1')
			RETURNING id
		`, headID).Scan(&attemptID)).To(Succeed())
		return headID, attemptID
	}

	createRecoveryAttempt := func(headID, firstAttemptID int64) int64 {
		GinkgoHelper()
		_, err := dbConn.Exec(`
			UPDATE agent_run_attempts
			SET state = 'interrupted', is_current = FALSE,
				interruption_reason = 'preempted', interrupted_at = clock_timestamp()
			WHERE id = $1
		`, firstAttemptID)
		Expect(err).NotTo(HaveOccurred())
		var attemptID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_run_attempts
				(head_id, attempt_number, state, is_current, materialization_id,
				 source_attempt_number, source_checkpoint_generation, recovery_mode,
				 source_interruption_reason)
			VALUES ($1, 2, 'scheduling', TRUE, 'transcript-materialization-2',
				1, 0, 'checkpoint_zero', 'preempted')
			RETURNING id
		`, headID).Scan(&attemptID)).To(Succeed())
		return attemptID
	}

	It("upserts on (build_id, plan_id): a re-ingest overwrites, never duplicates", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("upsert-workflow", "transcript-upsert-key", build.ID())

		nd := `{"type":"system","subtype":"init"}` + "\n" + `{"type":"result","total_cost_usd":0.4}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: build.ID(), PlanID: "aa11", WorkflowRunID: &runID,
			FunctionID: "implement", StepName: "implement",
			NDJSON: nd, ByteLen: len(nd), Truncated: false,
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("upsert-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].NDJSON).To(Equal(nd))
		Expect(rows[0].ByteLen).To(Equal(len(nd)))
		Expect(rows[0].Truncated).To(BeFalse())
		Expect(rows[0].FunctionID).To(Equal("implement"))

		// second write of the same key overwrites, does not duplicate
		nd2 := nd + `{"type":"extra"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: build.ID(), PlanID: "aa11", WorkflowRunID: &runID,
			FunctionID: "implement", StepName: "implement",
			NDJSON: nd2, ByteLen: len(nd2), Truncated: true,
		})).To(Succeed())

		rows, err = factory.ListByWorkflowRun("upsert-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].NDJSON).To(Equal(nd2))
		Expect(rows[0].Truncated).To(BeTrue())
	})

	It("returns nothing for a run that captured no transcript", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("empty-workflow", "transcript-empty-key", build.ID())

		rows, err := factory.ListByWorkflowRun("empty-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})

	It("lists transcripts by durable workflow run, surviving its build's deletion", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("code-review", "transcript-run-key", build.ID())

		nd := `{"type":"result","step":"review"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: build.ID(), PlanID: "p1", WorkflowRunID: &runID, FunctionID: "review",
			StepName: "review-diff", NDJSON: nd, ByteLen: len(nd),
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("code-review", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].WorkflowRunID).ToNot(BeNil())
		Expect(*rows[0].WorkflowRunID).To(Equal(runID))
		Expect(rows[0].FunctionID).To(Equal("review"))
		Expect(rows[0].StepName).To(Equal("review-diff"))
		Expect(rows[0].NDJSON).To(Equal(nd))

		By("returning nothing for a run id under the wrong workflow name")
		wrong, err := factory.ListByWorkflowRun("some-other-workflow", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(wrong).To(BeEmpty())

		By("keeping the transcript after its builds row is deleted")
		Expect(build.Delete()).To(BeTrue())
		rows, err = factory.ListByWorkflowRun("code-review", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(1))
		Expect(rows[0].NDJSON).To(Equal(nd))
	})

	It("orders a run's transcripts oldest-first, with no step-name heuristic", func() {
		firstBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		secondBuild, err := defaultTeam.CreateOneOffBuild()
		Expect(err).ToNot(HaveOccurred())
		runID := createWorkflowRun("multi-step", "transcript-multi-key", firstBuild.ID())

		// 'implement' is written FIRST and 'spec' second: the retired harvest-era
		// heuristic would have hoisted 'implement' to the front regardless of
		// time; v3 orders purely by creation.
		first := `{"type":"result","step":"implement"}` + "\n"
		second := `{"type":"result","step":"spec"}` + "\n"
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: firstBuild.ID(), PlanID: "implement-plan", WorkflowRunID: &runID, FunctionID: "implement",
			StepName: "implement", NDJSON: first, ByteLen: len(first),
		})).To(Succeed())
		Expect(factory.Upsert(db.AgentRunTranscript{
			BuildID: secondBuild.ID(), PlanID: "spec-plan", WorkflowRunID: &runID, FunctionID: "spec",
			StepName: "spec", NDJSON: second, ByteLen: len(second),
		})).To(Succeed())

		rows, err := factory.ListByWorkflowRun("multi-step", runID)
		Expect(err).ToNot(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].PlanID).To(Equal("implement-plan"))
		Expect(rows[1].PlanID).To(Equal("spec-plan"))
	})

	It("keeps recovery attempts separate and projects only the selected final attempt", func() {
		build, err := defaultTeam.CreateOneOffBuild()
		Expect(err).NotTo(HaveOccurred())
		runID := createWorkflowRun("transcript-attempts", "transcript-attempts-key", build.ID())
		headID, firstAttemptID := createAttempt(build.ID(), "attempt-plan", "implement", &runID)

		first := db.AgentRunAttemptTranscript{
			AttemptID: firstAttemptID, BuildID: build.ID(), PlanID: "attempt-plan", ExecutionAttempt: 1,
			WorkflowRunID: &runID, FunctionID: "implement", StepName: "implement",
			NDJSON: "interrupted\n", ByteLen: len("interrupted\n"),
		}
		Expect(factory.UpsertExecutionAttempt(first)).To(Succeed())
		var legacyCount int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_run_transcripts WHERE build_id = $1 AND plan_id = $2`, build.ID(), first.PlanID).Scan(&legacyCount)).To(Succeed())
		Expect(legacyCount).To(BeZero())

		secondAttemptID := createRecoveryAttempt(headID, firstAttemptID)
		final := first
		final.AttemptID = secondAttemptID
		final.ExecutionAttempt = 2
		final.NDJSON = "final\n"
		final.ByteLen = len(final.NDJSON)
		final.FinalPresentation = true
		Expect(factory.UpsertExecutionAttempt(final)).To(Succeed())

		var attempts, finals int
		Expect(dbConn.QueryRow(`
			SELECT count(*), count(*) FILTER (WHERE display_finalized)
			FROM agent_run_attempt_transcripts WHERE build_id = $1 AND plan_id = $2
		`, build.ID(), first.PlanID).Scan(&attempts, &finals)).To(Succeed())
		Expect(attempts).To(Equal(2))
		Expect(finals).To(Equal(1))
		var projection string
		Expect(dbConn.QueryRow(`SELECT ndjson FROM agent_run_transcripts WHERE build_id = $1 AND plan_id = $2`, build.ID(), first.PlanID).Scan(&projection)).To(Succeed())
		Expect(projection).To(Equal("final\n"))

		By("refreshing the selected attempt and projection on an idempotent retry")
		final.FinalPresentation = false
		final.NDJSON = "final-retry\n"
		final.ByteLen = len(final.NDJSON)
		Expect(factory.UpsertExecutionAttempt(final)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT ndjson FROM agent_run_transcripts WHERE build_id = $1 AND plan_id = $2`, build.ID(), first.PlanID).Scan(&projection)).To(Succeed())
		Expect(projection).To(Equal("final-retry\n"))
	})

	It("rejects a transcript whose attempt ID does not verify its exact durable identity", func() {
		headID, firstAttemptID := createAttempt(9201, "identity-plan", "implement", nil)
		secondAttemptID := createRecoveryAttempt(headID, firstAttemptID)
		request := db.AgentRunAttemptTranscript{
			AttemptID: secondAttemptID, BuildID: 9201, PlanID: "identity-plan", ExecutionAttempt: 2,
			FunctionID: "implement", StepName: "implement", NDJSON: "retry\n", ByteLen: len("retry\n"),
		}
		Expect(factory.UpsertExecutionAttempt(request)).To(Succeed())
		request.ExecutionAttempt = 1
		Expect(factory.UpsertExecutionAttempt(request)).To(MatchError(ContainSubstring("identity")))
		request.ExecutionAttempt = 2
		request.FunctionID = "review"
		Expect(factory.UpsertExecutionAttempt(request)).To(MatchError(ContainSubstring("identity")))
	})

	It("serializes final selection so another attempt cannot replace its presentation", func() {
		headID, firstAttemptID := createAttempt(9301, "final-plan", "implement", nil)
		secondAttemptID := createRecoveryAttempt(headID, firstAttemptID)
		requests := []db.AgentRunAttemptTranscript{
			{AttemptID: firstAttemptID, BuildID: 9301, PlanID: "final-plan", ExecutionAttempt: 1, FunctionID: "implement", StepName: "implement", NDJSON: "one\n", ByteLen: 4, FinalPresentation: true},
			{AttemptID: secondAttemptID, BuildID: 9301, PlanID: "final-plan", ExecutionAttempt: 2, FunctionID: "implement", StepName: "implement", NDJSON: "two\n", ByteLen: 4, FinalPresentation: true},
		}
		start := make(chan struct{})
		errs := make(chan error, len(requests))
		var group sync.WaitGroup
		for _, request := range requests {
			request := request
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				errs <- factory.UpsertExecutionAttempt(request)
			}()
		}
		close(start)
		group.Wait()
		close(errs)
		successes := 0
		for err := range errs {
			if err == nil {
				successes++
			} else {
				Expect(errors.Is(err, db.ErrAgentRunAttemptTranscriptFinalized)).To(BeTrue())
			}
		}
		Expect(successes).To(Equal(1))
	})

	It("rolls back a final attempt when legacy projection fails", func() {
		const trigger = "reject_attempt_transcript_aggregate_test"
		const function = "reject_attempt_transcript_aggregate_test_fn"
		_, err := dbConn.Exec(`CREATE FUNCTION ` + function + `() RETURNS trigger AS $$
			BEGIN RAISE EXCEPTION 'forced aggregate failure'; END;
		$$ LANGUAGE plpgsql`)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`CREATE TRIGGER ` + trigger + ` BEFORE INSERT ON agent_run_transcripts
			FOR EACH ROW EXECUTE FUNCTION ` + function + `()`)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = dbConn.Exec(`DROP TRIGGER IF EXISTS ` + trigger + ` ON agent_run_transcripts`)
			_, _ = dbConn.Exec(`DROP FUNCTION IF EXISTS ` + function + `()`)
		})

		_, attemptID := createAttempt(9401, "atomic-plan", "implement", nil)
		err = factory.UpsertExecutionAttempt(db.AgentRunAttemptTranscript{
			AttemptID: attemptID, BuildID: 9401, PlanID: "atomic-plan", ExecutionAttempt: 1,
			FunctionID: "implement", StepName: "implement", NDJSON: "final\n", ByteLen: 6, FinalPresentation: true,
		})
		Expect(err).To(HaveOccurred())
		var attempts, aggregate int
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_run_attempt_transcripts WHERE attempt_id = $1`, attemptID).Scan(&attempts)).To(Succeed())
		Expect(dbConn.QueryRow(`SELECT count(*) FROM agent_run_transcripts WHERE build_id = 9401 AND plan_id = 'atomic-plan'`).Scan(&aggregate)).To(Succeed())
		Expect(attempts).To(BeZero())
		Expect(aggregate).To(BeZero())
	})
})
