package db_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowrun"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentWorkflowWaitsFactory", func() {
	var (
		ctx          context.Context
		factory      db.AgentWorkflowWaitsFactory
		runs         db.AgentWorkflowRunsFactory
		run          db.AgentWorkflowRun
		buildID      int64
		question     snapshot.SnapshotRef
		defaultValue snapshot.SnapshotRef
		answers      []snapshot.SnapshotRef
	)

	insertSnapshot := func(typeName string, typeVersion int, digit string) snapshot.SnapshotRef {
		var id int64
		digest := "sha256:" + strings.Repeat(digit, 64)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count, representation)
			VALUES ($1, $2, $3, $4, 1, 1, 'application/x-tar')
			RETURNING id
		`, defaultTeam.ID(), typeName, typeVersion, digest).Scan(&id)).To(Succeed())
		typeRef, err := snapshot.ParseTypeRef(fmt.Sprintf("%s/v%d", typeName, typeVersion))
		Expect(err).NotTo(HaveOccurred())
		return snapshot.SnapshotRef{ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest)}
	}

	BeforeEach(func() {
		ctx = context.Background()
		factory = db.NewAgentWorkflowWaitsFactory(dbConn, 36*time.Hour)
		runs = db.NewAgentWorkflowRunsFactory(dbConn)
		definitionName := fmt.Sprintf("workflow-wait-%d", time.Now().UnixNano())
		definitionSource := fmt.Sprintf(`schema_version: 3
name: %s
signature_version: 1
inputs: []
outputs: []
plan:
  - agent: work
    function_id: work
    prompt: test
`, definitionName)
		var definitionID int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, $3, 'alice', 3, 1)
			RETURNING id
		`, definitionName, strings.Repeat("a", 64), definitionSource).Scan(&definitionID)).To(Succeed())
		var created bool
		var err error
		run, created, err = runs.CreateWithInputs(ctx, db.AgentWorkflowRunCreateRequest{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), WorkflowDefinitionID: definitionID,
			WorkflowName: definitionName, WorkflowVersion: 1, SchemaVersion: 3, SignatureVersion: 1,
			DefinitionContentHash: strings.Repeat("a", 64), IdempotencyKey: fmt.Sprintf("wait-%d", time.Now().UnixNano()),
			ParameterizedConfig: json.RawMessage(`{"jobs":[{"name":"run"}]}`), ParameterizedConfigHash: strings.Repeat("b", 64),
			OriginKind: "manual", CreatedBy: "alice", Status: db.AgentWorkflowRunStatusAdmitting,
			Inputs: map[string]snapshot.SnapshotRef{},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		var templateID, instanceID, pipelineRunID int
		unique := fmt.Sprintf("workflow-wait-exec-%d", time.Now().UnixNano())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, unique+"-template", defaultTeam.ID()).Scan(&templateID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipelines (name, team_id, secondary_ordering) VALUES ($1, $2, 1) RETURNING id`, unique+"-instance", defaultTeam.ID()).Scan(&instanceID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number) VALUES ($1, $2, 1) RETURNING id`, templateID, instanceID).Scan(&pipelineRunID)).To(Succeed())
		Expect(dbConn.QueryRow(`INSERT INTO builds (name, status, team_id, pipeline_id) VALUES ($1, 'pending', $2, $3) RETURNING id`, unique+"-build", defaultTeam.ID(), instanceID).Scan(&buildID)).To(Succeed())
		Expect(runs.LinkExecution(ctx, run.ID, db.AgentWorkflowRunExecutionLink{
			PipelineRunID: pipelineRunID, TemplatePipelineID: templateID, InstancePipelineID: instanceID,
			ConcreteConfig: json.RawMessage(`{"jobs":[{"name":"run"}]}`), ConcreteConfigHash: strings.Repeat("c", 64),
		})).To(Succeed())
		_, err = dbConn.Exec(`UPDATE agent_workflow_runs SET planned_build_id = $2 WHERE id = $1`, int64(run.ID), buildID)
		Expect(err).NotTo(HaveOccurred())
		advanced, err := runs.Transition(ctx, run.ID, db.AgentWorkflowRunStatusAdmitting, db.AgentWorkflowRunStatusRunning, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced).To(BeTrue())

		question = insertSnapshot("question", 1, "1")
		defaultValue = insertSnapshot("human-answer", 1, "2")
		answers = []snapshot.SnapshotRef{
			insertSnapshot("human-answer", 1, "3"),
			insertSnapshot("human-answer", 1, "4"),
		}
	})

	request := func(deadline time.Time, planID, output string) workflowwait.CreateRequest {
		defaultCopy := defaultValue
		return workflowwait.CreateRequest{
			Key: workflowwait.ExecutionKey{
				TeamID: defaultTeam.ID(), WorkflowRunID: run.ID, BuildID: buildID,
				PlanID: planID, Attempt: "0", OutputName: output,
			},
			QuestionName: "question", Question: question, ExpectedType: "human-answer/v1",
			Deadline: deadline, TimeoutPolicy: workflowwait.TimeoutDefault, Default: &defaultCopy,
			WorkflowPort: "approval", WorkflowDefinitionID: run.WorkflowDefinitionID,
		}
	}
	resolve := func(
		ctx context.Context,
		wait workflowwait.Wait,
		answer snapshot.SnapshotRef,
		answerValue string,
		actor string,
		displayName string,
	) (workflowwait.Wait, bool, error) {
		_, intent, found, err := factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			AnswerValue:   answerValue,
			Actor:         actor,
			DisplayName:   displayName,
		})
		if err != nil || !found {
			return workflowwait.Wait{}, found, err
		}
		return factory.Resolve(ctx, workflowwait.ResolveRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			Answer:        answer,
			AnswerValue:   intent.AnswerValue,
			Actor:         intent.Actor,
			DisplayName:   intent.DisplayName,
			ReservedAt:    intent.ReservedAt,
		})
	}

	It("creates one exact durable wait and preserves the first deadline across restart", func() {
		firstDeadline := time.Now().Add(time.Hour).Round(time.Microsecond)
		created, fresh, err := factory.CreateOrGet(ctx, request(firstDeadline, "plan-1", "answer"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(BeTrue())
		Expect(created.Deadline).To(BeTemporally("==", firstDeadline))

		restarted, fresh, err := factory.CreateOrGet(ctx, request(firstDeadline.Add(time.Hour), "plan-1", "answer"))
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh).To(BeFalse())
		Expect(restarted.ID).To(Equal(created.ID))
		Expect(restarted.Deadline).To(BeTemporally("==", firstDeadline))

		listed, err := factory.List(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(ConsistOf(restarted))
		_, found, err := factory.Get(ctx, defaultTeam.ID()+1, run.ID, created.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())

		for _, kind := range []string{"question", "default"} {
			var claims int
			Expect(dbConn.QueryRow(`
				SELECT count(*) FROM agent_snapshot_retention_claims
				WHERE team_id = $1 AND class = 'run' AND workflow_run_id = $2 AND actor = $3
			`, defaultTeam.ID(), int64(run.ID),
				fmt.Sprintf("workflow-run:%d:wait:%d:%s", int64(run.ID), int64(created.ID), kind)).Scan(&claims)).To(Succeed())
			Expect(claims).To(Equal(1))
		}
	})

	It("retains an internal answer only for the active workflow run", func() {
		internal := request(time.Now().Add(time.Hour), "internal", "answer")
		internal.WorkflowPort = ""
		internal.WorkflowDefinitionID = 0
		wait, _, err := factory.CreateOrGet(ctx, internal)
		Expect(err).NotTo(HaveOccurred())
		resolved, found, err := resolve(
			ctx, wait, answers[0], "approve", "subject:sha256:alice", "Alice",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		var runClaims, workflowClaims, bindingClaims int
		var bindingExpiresAt time.Time
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'run' AND workflow_run_id = $2
			  AND actor = $3
		`, int64(resolved.Answer.ID), int64(run.ID),
			fmt.Sprintf("workflow-run:%d:wait:%d", int64(run.ID), int64(wait.ID))).Scan(&runClaims)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'workflow'
		`, int64(resolved.Answer.ID)).Scan(&workflowClaims)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*), max(expires_at) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'binding'
			  AND actor = $2
		`, int64(resolved.Answer.ID),
			fmt.Sprintf("workflow-run:%d:wait:%d", int64(run.ID), int64(wait.ID))).Scan(
			&bindingClaims, &bindingExpiresAt,
		)).To(Succeed())
		Expect(runClaims).To(Equal(1))
		Expect(workflowClaims).To(Equal(0))
		Expect(bindingClaims).To(Equal(1))
		Expect(bindingExpiresAt).To(BeTemporally("~", time.Now().Add(36*time.Hour), time.Minute))

		_, finalized, err := runs.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			TerminalStatus: db.AgentWorkflowRunStatusErrored, ErrorMessage: "test terminalization",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'run' AND workflow_run_id = $2
		`, int64(resolved.Answer.ID), int64(run.ID)).Scan(&runClaims)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'binding'
			  AND actor = $2
		`, int64(resolved.Answer.ID),
			fmt.Sprintf("workflow-run:%d:wait:%d", int64(run.ID), int64(wait.ID))).Scan(&bindingClaims)).To(Succeed())
		Expect(runClaims).To(Equal(0))
		Expect(bindingClaims).To(Equal(1))
	})

	It("does not accept or materialize human answers after terminalization wins", func() {
		reservedRequest := request(time.Now().Add(time.Hour), "reserved-before-terminal", "reserved-answer")
		reservedRequest.WorkflowPort = ""
		reservedRequest.WorkflowDefinitionID = 0
		reservedWait, _, err := factory.CreateOrGet(ctx, reservedRequest)
		Expect(err).NotTo(HaveOccurred())

		lateRequest := request(time.Now().Add(time.Hour), "late-after-terminal", "late-answer")
		lateRequest.WorkflowPort = ""
		lateRequest.WorkflowDefinitionID = 0
		lateWait, _, err := factory.CreateOrGet(ctx, lateRequest)
		Expect(err).NotTo(HaveOccurred())

		_, intent, found, err := factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID: defaultTeam.ID(), WorkflowRunID: run.ID, WaitID: reservedWait.ID,
			AnswerValue: "approve", Actor: "subject:sha256:alice", DisplayName: "Alice",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		_, finalized, err := runs.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID: run.ID, ExpectedStatus: db.AgentWorkflowRunStatusRunning,
			TerminalStatus: db.AgentWorkflowRunStatusErrored, ErrorMessage: "terminal state won",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())

		_, _, found, err = factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID: defaultTeam.ID(), WorkflowRunID: run.ID, WaitID: lateWait.ID,
			AnswerValue: "approve", Actor: "subject:sha256:bob", DisplayName: "Bob",
		})
		Expect(found).To(BeTrue())
		Expect(err).To(MatchError(workflowwait.ErrConflict))

		_, found, err = factory.Resolve(ctx, workflowwait.ResolveRequest{
			TeamID: defaultTeam.ID(), WorkflowRunID: run.ID, WaitID: reservedWait.ID,
			Answer: answers[0], AnswerValue: intent.AnswerValue, Actor: intent.Actor,
			DisplayName: intent.DisplayName, ReservedAt: intent.ReservedAt,
		})
		Expect(found).To(BeTrue())
		Expect(err).To(MatchError(workflowwait.ErrConflict))

		var runClaims, productions int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_retention_claims
			WHERE snapshot_id = $1 AND class = 'run' AND workflow_run_id = $2
		`, int64(answers[0].ID), int64(run.ID)).Scan(&runClaims)).To(Succeed())
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_snapshot_productions
			WHERE snapshot_id = $1 AND workflow_run_id = $2
		`, int64(answers[0].ID), int64(run.ID)).Scan(&productions)).To(Succeed())
		Expect(runClaims).To(Equal(0))
		Expect(productions).To(Equal(0))
	})

	It("authorizes the exact run build and snapshot tuple", func() {
		wrongBuild := request(time.Now().Add(time.Hour), "wrong-build", "answer")
		wrongBuild.Key.BuildID++
		_, _, err := factory.CreateOrGet(ctx, wrongBuild)
		Expect(err).To(MatchError(workflowwait.ErrUnavailable))

		wrongQuestion := request(time.Now().Add(time.Hour), "wrong-question", "answer")
		wrongQuestion.Question = answers[0]
		_, _, err = factory.CreateOrGet(ctx, wrongQuestion)
		Expect(err).To(MatchError(workflowwait.ErrUnavailable))

		wrongDefault := request(time.Now().Add(time.Hour), "wrong-default", "answer")
		wrongDefault.Default = &question
		_, _, err = factory.CreateOrGet(ctx, wrongDefault)
		Expect(err).To(HaveOccurred())
	})

	It("allows exactly one authorized human answer and records output evidence atomically", func() {
		wait, _, err := factory.CreateOrGet(ctx, request(time.Now().Add(time.Hour), "race", "answer"))
		Expect(err).NotTo(HaveOccurred())

		type outcome struct {
			wait  workflowwait.Wait
			found bool
			err   error
		}
		results := make(chan outcome, 16)
		var group sync.WaitGroup
		for index := 0; index < 16; index++ {
			group.Add(1)
			go func(index int) {
				defer group.Done()
				value, found, resolveErr := resolve(
					context.Background(),
					wait,
					answers[index%2],
					fmt.Sprintf("answer-%d", index%2),
					fmt.Sprintf("subject:sha256:human-%d", index),
					fmt.Sprintf("Human %d", index),
				)
				results <- outcome{wait: value, found: found, err: resolveErr}
			}(index)
		}
		group.Wait()
		close(results)
		wins := 0
		var winner workflowwait.Wait
		for result := range results {
			if result.err == nil {
				wins++
				winner = result.wait
			} else {
				Expect(errors.Is(result.err, workflowwait.ErrConflict)).To(BeTrue(), result.err.Error())
			}
			Expect(result.found).To(BeTrue())
		}
		Expect(wins).To(Equal(1))
		Expect(winner.Answer).NotTo(BeNil())

		var bindingID int64
		Expect(dbConn.QueryRow(`
			SELECT snapshot_id FROM agent_workflow_run_snapshots
			WHERE workflow_run_id = $1 AND direction = 'output' AND port_name = 'approval'
		`, int64(run.ID)).Scan(&bindingID)).To(Succeed())
		Expect(bindingID).To(Equal(int64(winner.Answer.ID)))
		var productionID, lineageID int64
		var stepKind string
		Expect(dbConn.QueryRow(`
			SELECT id, step_kind FROM agent_snapshot_productions
			WHERE build_id = $1 AND plan_id = 'race' AND attempt = '0' AND output_port = 'answer'
		`, buildID).Scan(&productionID, &stepKind)).To(Succeed())
		Expect(stepKind).To(Equal("await_snapshot"))
		Expect(dbConn.QueryRow(`SELECT input_snapshot_id FROM agent_snapshot_lineage WHERE production_id = $1`, productionID).Scan(&lineageID)).To(Succeed())
		Expect(lineageID).To(Equal(int64(question.ID)))

		// Every recorded lineage input carries exposure lineage, so "what was
		// this production shown?" has no holes. The answering party saw the
		// whole question tree, and nothing was mounted into a filesystem.
		var mode, treeDigest string
		var mountPath sql.NullString
		Expect(dbConn.QueryRow(`
			SELECT materialization_mode, tree_digest, mount_path
			FROM agent_snapshot_exposures WHERE production_id = $1 AND input_port = $2
		`, productionID, "question").Scan(&mode, &treeDigest, &mountPath)).To(Succeed())
		Expect(mode).To(Equal("full"))
		Expect(treeDigest).To(Equal(question.Digest.String()))
		Expect(mountPath.Valid).To(BeFalse())
	})

	It("keeps a pre-deadline reservation replayable after the deadline", func() {
		wait, _, err := factory.CreateOrGet(ctx, request(time.Now().Add(time.Hour), "reserved-deadline", "answer"))
		Expect(err).NotTo(HaveOccurred())
		_, intent, found, err := factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			AnswerValue:   "approve",
			Actor:         "subject:sha256:alice",
			DisplayName:   "Alice",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		_, err = dbConn.Exec(`
			UPDATE agent_workflow_waits
			SET deadline = now() - interval '1 minute'
			WHERE id = $1
		`, int64(wait.ID))
		Expect(err).NotTo(HaveOccurred())

		_, replayed, found, err := factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			AnswerValue:   "approve",
			Actor:         "subject:sha256:alice",
			DisplayName:   "Alice renamed after reservation",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(replayed).To(Equal(intent))

		_, _, found, err = factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			AnswerValue:   "reject",
			Actor:         "subject:sha256:alice",
			DisplayName:   "Alice",
		})
		Expect(found).To(BeTrue())
		Expect(err).To(MatchError(workflowwait.ErrConflict))

		stillWaiting, found, err := factory.Expire(ctx, wait.Key, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(stillWaiting.Status).To(Equal(workflowwait.StatusWaiting))

		restartedFactory := db.NewAgentWorkflowWaitsFactory(dbConn)
		pending, err := restartedFactory.PendingResolutions(ctx, defaultTeam.ID(), run.ID, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(Equal([]workflowwait.PendingResolution{{Wait: stillWaiting, Intent: intent}}))

		resolved, found, err := restartedFactory.Resolve(ctx, workflowwait.ResolveRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			Answer:        answers[0],
			AnswerValue:   replayed.AnswerValue,
			Actor:         replayed.Actor,
			DisplayName:   replayed.DisplayName,
			ReservedAt:    replayed.ReservedAt,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(resolved.Status).To(Equal(workflowwait.StatusResolved))
		pending, err = restartedFactory.PendingResolutions(ctx, defaultTeam.ID(), run.ID, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending).To(BeEmpty())
	})

	It("keeps completed wait and run history readable after the ephemeral build is deleted", func() {
		wait, _, err := factory.CreateOrGet(ctx, request(time.Now().Add(time.Hour), "garbage-collected", "answer"))
		Expect(err).NotTo(HaveOccurred())
		resolved, found, err := resolve(
			ctx,
			wait,
			answers[0],
			"approve",
			"subject:sha256:alice",
			"Alice",
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())

		_, intent, found, err := factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			AnswerValue:   "approve",
			Actor:         "subject:sha256:alice",
			DisplayName:   "Alice renamed after resolution",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(intent.DisplayName).To(Equal("Alice"), "the first audit display name is immutable")

		replay := workflowwait.ResolveRequest{
			TeamID:        defaultTeam.ID(),
			WorkflowRunID: run.ID,
			WaitID:        wait.ID,
			Answer:        answers[0],
			AnswerValue:   intent.AnswerValue,
			Actor:         intent.Actor,
			DisplayName:   intent.DisplayName,
			ReservedAt:    intent.ReservedAt,
		}
		_, found, err = factory.Resolve(ctx, replay)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		replay.DisplayName = "forged replay display"
		_, found, err = factory.Resolve(ctx, replay)
		Expect(found).To(BeTrue())
		Expect(err).To(MatchError(workflowwait.ErrConflict))

		_, err = dbConn.Exec(`UPDATE builds SET status = 'succeeded', end_time = now() WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`DELETE FROM builds WHERE id = $1`, buildID)
		Expect(err).NotTo(HaveOccurred())

		storedWait, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID, wait.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(storedWait.Key).To(Equal(wait.Key), "the copied execution identity is durable evidence")
		Expect(storedWait.Status).To(Equal(workflowwait.StatusResolved))
		Expect(storedWait.Answer).To(Equal(resolved.Answer))
		listed, err := factory.List(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(ConsistOf(storedWait))

		storedRun, found, err := runs.Get(ctx, defaultTeam.ID(), run.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(storedRun.ID).To(Equal(run.ID))
		Expect(storedRun.PlannedBuildID).To(Equal(&buildID), "the run retains its copied build identity")
	})

	It("makes timeout and cancellation durable terminal winners", func() {
		past := time.Now().Add(-time.Minute)
		defaultWait, _, err := factory.CreateOrGet(ctx, request(past, "default-timeout", "default-answer"))
		Expect(err).NotTo(HaveOccurred())
		timedOut, found, err := factory.Expire(ctx, defaultWait.Key, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(timedOut.Status).To(Equal(workflowwait.StatusTimedOut))
		Expect(timedOut.Answer).To(Equal(&defaultValue))
		_, _, found, err = factory.ReserveResolution(ctx, workflowwait.ReserveResolutionRequest{
			TeamID: defaultTeam.ID(), WorkflowRunID: run.ID, WaitID: defaultWait.ID,
			AnswerValue: "late", Actor: "subject:sha256:late-human", DisplayName: "Late Human",
		})
		Expect(found).To(BeTrue())
		Expect(err).To(MatchError(workflowwait.ErrConflict))

		failRequest := request(past, "fail-timeout", "failed-answer")
		failRequest.TimeoutPolicy, failRequest.Default = workflowwait.TimeoutFail, nil
		failRequest.WorkflowPort, failRequest.WorkflowDefinitionID = "", 0
		failedWait, _, err := factory.CreateOrGet(ctx, failRequest)
		Expect(err).NotTo(HaveOccurred())
		failedWait, _, err = factory.Expire(ctx, failedWait.Key, time.Now())
		Expect(err).NotTo(HaveOccurred())
		Expect(failedWait.Status).To(Equal(workflowwait.StatusTimedOut))
		Expect(failedWait.Answer).To(BeNil())

		open, _, err := factory.CreateOrGet(ctx, request(time.Now().Add(time.Hour), "cancel", "cancel-answer"))
		Expect(err).NotTo(HaveOccurred())
		advanced, err := runs.Transition(ctx, run.ID, db.AgentWorkflowRunStatusRunning, db.AgentWorkflowRunStatusCanceling, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(advanced).To(BeTrue())
		_, finalized, err := runs.Finalize(ctx, db.AgentWorkflowRunFinalization{
			WorkflowRunID:  run.ID,
			ExpectedStatus: db.AgentWorkflowRunStatusCanceling,
			TerminalStatus: db.AgentWorkflowRunStatusAborted,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(finalized).To(BeTrue())
		claimed, err := runs.ClaimForReconciliation(ctx, time.Now().Add(time.Hour), time.Minute, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).To(ContainElement(run.ID), "an aborted run with an open wait must remain repairable")
		restartClock := time.Now().Add(2 * time.Hour)
		reconciler, err := workflowrun.NewReconciler(
			runs,
			lager.NewLogger("workflow-wait-cancellation-repair"),
			15*time.Minute,
			time.Minute,
			workflowrun.WithReconcilerClock(func() time.Time { return restartClock }),
			workflowrun.WithWaitCanceler(factory),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(reconciler.Run(ctx)).To(Succeed())
		cancelled, found, err := factory.Get(ctx, defaultTeam.ID(), run.ID, open.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(cancelled.Status).To(Equal(workflowwait.StatusCancelled))
		claimed, err = runs.ClaimForReconciliation(ctx, restartClock.Add(time.Hour), time.Minute, 100)
		Expect(err).NotTo(HaveOccurred())
		Expect(claimed).NotTo(ContainElement(run.ID), "a repaired terminal run must leave the claim set")
	})
})
