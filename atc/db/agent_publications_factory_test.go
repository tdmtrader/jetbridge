package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
	"github.com/concourse/concourse/atc/db"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("AgentPublicationsFactory", func() {
	var factory db.AgentPublicationsFactory
	var input snapshot.SnapshotRef
	var workflowRunID snapshot.WorkflowRunID
	var buildID int64
	var definitionID int
	var newApproval func(actor, planID, questionDigestCharacter, answerDigestCharacter string, resolvedAt time.Time) *publisher.ApprovalEvidence

	BeforeEach(func() {
		factory = db.NewAgentPublicationsFactory(dbConn)
		unique := time.Now().UnixNano()
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by, schema_version, signature_version)
			VALUES ($1, 1, $2, 'schema_version: 3', 'alice', 3, 1)
			RETURNING id
		`, "publication-workflow-"+fmt.Sprint(unique), strings.Repeat("d", 64)).Scan(&definitionID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'alice') RETURNING id
		`, "publication-build-"+fmt.Sprint(unique), defaultTeam.ID()).Scan(&buildID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'alice', 'running', $8)
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID, "publication-workflow-"+fmt.Sprint(unique),
			strings.Repeat("d", 64), "publication-"+fmt.Sprint(unique), strings.Repeat("e", 64), buildID,
		).Scan(&workflowRunID)).To(Succeed())
		var id int64
		digest := "sha256:" + strings.Repeat("a", 64)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ('repository-change', 1, $1, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, digest).Scan(&id)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'alice', 'publication test')
		`, id, defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'output', 'change', $2, now())
		`, int64(workflowRunID), id)
		Expect(err).NotTo(HaveOccurred())
		input = snapshot.SnapshotRef{ID: snapshot.SnapshotID(id), Type: "repository-change/v1", Digest: snapshot.Digest(digest)}

		newApproval = func(actor, planID, questionDigestCharacter, answerDigestCharacter string, resolvedAt time.Time) *publisher.ApprovalEvidence {
			var questionID, answerID, waitID int64
			questionDigest := "sha256:" + strings.Repeat(questionDigestCharacter, 64)
			answerDigest := "sha256:" + strings.Repeat(answerDigestCharacter, 64)
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation, content_state)
				VALUES ('question', 1, $1, 1, 1, 'application/x-tar', 'available')
				RETURNING id
			`, questionDigest).Scan(&questionID)).To(Succeed())
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(type_name, type_version, digest, byte_size, file_count, representation, content_state)
				VALUES ('human-answer', 1, $1, 1, 1, 'application/x-tar', 'available')
				RETURNING id
			`, answerDigest).Scan(&answerID)).To(Succeed())
			_, err := dbConn.Exec(`
				INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
				VALUES ($1, $3, $4, 'publication approval test'),
				       ($2, $3, $4, 'publication approval test')
			`, questionID, answerID, defaultTeam.ID(), actor)
			Expect(err).NotTo(HaveOccurred())
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_workflow_waits
					(team_id, workflow_run_id, build_id, build_id_evidence,
					 plan_id, attempt, output_name, question_name, question_snapshot_id,
					 expected_type_name, expected_type_version, deadline, timeout_policy,
					 status, answer_snapshot_id, resolved_by, resolved_by_display_name,
					 resolution_source, resolved_at, resolution_intent_answer,
					 resolution_intent_actor, resolution_intent_display_name, resolution_intent_at)
				VALUES ($1, $2, $3, $3, $4, '1', 'approval', 'merge approval', $5,
				        'human-answer', 1, $6, 'fail', 'resolved', $7, $8, $8,
				        'human', $9, 'approve', $8, $8, $9)
				RETURNING id
			`, defaultTeam.ID(), int64(workflowRunID), buildID, planID, questionID,
				resolvedAt.Add(time.Hour), answerID, actor, resolvedAt).Scan(&waitID)).To(Succeed())
			return &publisher.ApprovalEvidence{
				WaitID:     workflowwait.ID(waitID),
				Question:   snapshot.SnapshotRef{ID: snapshot.SnapshotID(questionID), Type: "question/v1", Digest: snapshot.Digest(questionDigest)},
				Answer:     snapshot.SnapshotRef{ID: snapshot.SnapshotID(answerID), Type: "human-answer/v1", Digest: snapshot.Digest(answerDigest)},
				ResolvedBy: actor, ResolvedAt: resolvedAt,
			}
		}
	})

	request := func() publisher.Request {
		return publisher.Request{
			Publisher: publisher.GitPublisher, Input: input, Destination: "github.example/team/repo",
			Mode:                  publisher.ModePullRequest,
			Parameters:            map[string]string{"source_branch": "agent/change", "target_branch": "main"},
			ApprovalPolicyVersion: "engineering/v2",
			Authority: publisher.Authority{
				TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(), BuildID: buildID, Actor: "alice",
			},
		}
	}

	It("acquires once, reclaims an expired lease, and completes exactly one attempt", func() {
		first, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		Expect(first.ID).To(BeNumerically(">", 0))
		Expect(first.Attempt).To(Equal(1))
		Expect(first.Request.Authority.WorkflowRunID).To(Equal(workflowRunID))

		duplicate, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(duplicate.ID).To(Equal(first.ID))

		_, err = dbConn.Exec(`UPDATE agent_publications SET lease_until = now() - interval '1 second' WHERE id = $1`, first.ID)
		Expect(err).NotTo(HaveOccurred())
		reclaimed, execute, err := db.NewAgentPublicationsFactory(dbConn).Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		Expect(reclaimed.Attempt).To(Equal(2))

		_, err = factory.Complete(context.Background(), first.OperationKey, 1, publisher.Result{Status: publisher.StatusSucceeded})
		Expect(errors.Is(err, publisher.ErrOperationConflict)).To(BeTrue())
		result := publisher.Result{Status: publisher.StatusSucceeded, ExternalID: "pr-17", URL: "https://github.example/pr/17"}
		completed, err := factory.Complete(context.Background(), first.OperationKey, 2, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Status).To(Equal(publisher.StatusSucceeded))
		Expect(completed.Result).To(Equal(result))
		var (
			outcomePublicationID int64
			outcomeState         string
			outcomeDisposition   string
			outcomeRevision      int64
		)
		Expect(dbConn.QueryRow(`
			SELECT publication_id, publication_state, disposition, revision
			FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID)).Scan(
			&outcomePublicationID, &outcomeState, &outcomeDisposition, &outcomeRevision,
		)).To(Succeed())
		Expect(outcomePublicationID).To(Equal(int64(completed.ID)))
		Expect(outcomeState).To(Equal("published"))
		Expect(outcomeDisposition).To(Equal("accepted"))

		replayed, err := db.NewAgentPublicationsFactory(dbConn).Complete(context.Background(), first.OperationKey, 2, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(Equal(completed))
		var replayedRevision int64
		Expect(dbConn.QueryRow(`
			SELECT revision
			FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID)).Scan(&replayedRevision)).To(Succeed())
		Expect(replayedRevision).To(Equal(outcomeRevision), "idempotent completion must not revise the durable outcome link")

		outcomes := db.NewAgentWorkflowOutcomesFactory(dbConn)
		watcherReplay, created, err := outcomes.Record(context.Background(), defaultTeam.ID(), workflowoutcomes.RecordRequest{
			WorkflowRunID: workflowRunID, OutputSnapshotID: input.ID,
			Disposition: workflowoutcomes.DispositionAccepted, PublicationState: workflowoutcomes.PublicationNotRequested,
			Labels: []string{}, Actor: "alice",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(watcherReplay.PublicationState).To(Equal(workflowoutcomes.PublicationPublished))
		Expect(watcherReplay.PublicationID).NotTo(BeNil())
		Expect(watcherReplay.Revision).To(Equal(outcomeRevision), "watcher replay must ignore publisher-owned evidence")

		modified, created, err := outcomes.Modify(context.Background(), defaultTeam.ID(), workflowoutcomes.ModifyRequest{
			WorkflowRunID: workflowRunID, OutputSnapshotID: input.ID,
			Disposition: workflowoutcomes.DispositionRejected,
			Labels:      []string{"human-observation"}, Actor: "bob",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(modified.PublicationState).To(Equal(workflowoutcomes.PublicationPublished))
		Expect(modified.PublicationID).NotTo(BeNil())
		Expect(*modified.PublicationID).To(Equal(completed.ID))

		_, execute, err = factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
	})

	It("completes a terminally rejected publication as failed, publishes no outcome, and never reclaims it", func() {
		acquired, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())

		result := publisher.Result{
			Status: publisher.StatusFailed,
			Detail: "publish repository change: gateway rejected /v1/git/publish with status 403; retrying the identical request cannot succeed",
		}
		failed, err := factory.Complete(context.Background(), acquired.OperationKey, acquired.Attempt, result)
		Expect(err).NotTo(HaveOccurred())
		Expect(failed.Status).To(Equal(publisher.StatusFailed))
		Expect(failed.Result).To(Equal(result))
		Expect(failed.LeaseUntil).To(BeZero())

		// A terminal row's lease_until is unconditionally NULL: the table's own
		// CHECK constraint (status <> 'pending' AND lease_until IS NULL) makes
		// it impossible to force an "expired lease" onto a failed row the way
		// the pending-row lifecycle test does above, and Acquire's terminal
		// branch returns before it ever consults lease_until (it is checked
		// first, unconditionally). There is no lease left to expire, at any
		// distance in time, which is what ends the retry-forever loop for a
		// gateway rejection.
		reacquired, execute, err := db.NewAgentPublicationsFactory(dbConn).Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(reacquired.Status).To(Equal(publisher.StatusFailed))
		Expect(reacquired.Attempt).To(Equal(failed.Attempt))

		var outcomes int
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID)).Scan(&outcomes)).To(Succeed())
		Expect(outcomes).To(Equal(0), "a failed publication must not claim a published workflow outcome")

		replayed, err := db.NewAgentPublicationsFactory(dbConn).Complete(
			context.Background(), acquired.OperationKey, acquired.Attempt, result,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed).To(Equal(failed))
	})

	It("serializes concurrent first acquisition and clones request maps", func() {
		const contenders = 12
		results := make(chan bool, contenders)
		errorsSeen := make(chan error, contenders)
		var group sync.WaitGroup
		for range contenders {
			group.Add(1)
			go func() {
				defer group.Done()
				_, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
				results <- execute
				errorsSeen <- err
			}()
		}
		group.Wait()
		close(results)
		close(errorsSeen)
		wins := 0
		for execute := range results {
			if execute {
				wins++
			}
		}
		Expect(wins).To(Equal(1))
		for err := range errorsSeen {
			Expect(err).NotTo(HaveOccurred())
		}

		value := request()
		stored, _, err := factory.Acquire(context.Background(), value, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		value.Parameters["target_branch"] = "mutated"
		Expect(stored.Request.Parameters["target_branch"]).To(Equal("main"))
	})

	It("aliases one semantic terminal operation to each authorized workflow occurrence", func() {
		first, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		result := publisher.Result{
			Status: publisher.StatusSucceeded, ExternalID: "pr-shared",
			URL: "https://github.example/pr/shared",
		}

		var secondBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'bob') RETURNING id
		`, fmt.Sprintf("publication-replay-build-%d", time.Now().UnixNano()), defaultTeam.ID()).Scan(&secondBuildID)).To(Succeed())
		var secondRunID snapshot.WorkflowRunID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(team_id, team_name, workflow_definition_id, workflow_name, workflow_version,
				 schema_version, signature_version, definition_content_hash, idempotency_key,
				 parameterized_config, parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id)
			VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6, '{}', $7,
			        'manual', '', 'bob', 'running', $8)
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), definitionID,
			fmt.Sprintf("publication-replay-%d", time.Now().UnixNano()), strings.Repeat("d", 64),
			fmt.Sprintf("publication-replay-%d", time.Now().UnixNano()), strings.Repeat("e", 64),
			secondBuildID).Scan(&secondRunID)).To(Succeed())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'output', 'change', $2, now())
		`, int64(secondRunID), int64(input.ID))
		Expect(err).NotTo(HaveOccurred())

		replayRequest := request()
		replayRequest.Authority.BuildID = secondBuildID
		replayRequest.Authority.Actor = "bob"
		replayed, execute, err := factory.Acquire(context.Background(), replayRequest, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(replayed.ID).NotTo(Equal(first.ID), "each workflow occurrence needs exact durable evidence")
		Expect(replayed.OperationKey).To(Equal(first.OperationKey), "provider idempotency remains semantic")
		Expect(replayed.Status).To(Equal(publisher.StatusPending))
		Expect(replayed.Request.Authority.WorkflowRunID).To(Equal(secondRunID))
		Expect(replayed.Request.Authority.BuildID).To(Equal(secondBuildID))
		Expect(replayed.Request.Authority.Actor).To(Equal("bob"))

		first, err = factory.Complete(context.Background(), first.OperationKey, first.Attempt, result)
		Expect(err).NotTo(HaveOccurred())
		var (
			operationCount  int
			occurrenceCount int
			outcomeID       int64
		)
		Expect(dbConn.QueryRow(`
			SELECT count(*) FROM agent_publications WHERE operation_key = $1
		`, first.OperationKey).Scan(&operationCount)).To(Succeed())
		Expect(operationCount).To(Equal(1))
		Expect(dbConn.QueryRow(`
			SELECT count(*)
			FROM agent_publication_occurrences occurrence
			JOIN agent_publications publication ON publication.id = occurrence.publication_id
			WHERE publication.operation_key = $1
		`, first.OperationKey).Scan(&occurrenceCount)).To(Succeed())
		Expect(occurrenceCount).To(Equal(2))
		Expect(dbConn.QueryRow(`
			SELECT publication_id
			FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(secondRunID), int64(input.ID)).Scan(&outcomeID)).To(Succeed())
		Expect(outcomeID).To(Equal(int64(replayed.ID)))

		terminalReplay, execute, err := factory.Acquire(context.Background(), replayRequest, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(terminalReplay.ID).To(Equal(replayed.ID))
		Expect(terminalReplay.Status).To(Equal(publisher.StatusSucceeded))
		Expect(terminalReplay.Result).To(Equal(result))
		Expect(terminalReplay.Request.Authority).To(Equal(replayed.Request.Authority))
	})

	It("does not roll outcome evidence back when an older semantic completion is replayed", func() {
		first, execute, err := factory.Acquire(context.Background(), request(), time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		firstResult := publisher.Result{Status: publisher.StatusSucceeded, ExternalID: "pr-old"}
		first, err = factory.Complete(context.Background(), first.OperationKey, first.Attempt, firstResult)
		Expect(err).NotTo(HaveOccurred())

		newerRequest := request()
		newerRequest.Destination = "github.example/team/new-repo"
		newer, execute, err := factory.Acquire(context.Background(), newerRequest, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		newerResult := publisher.Result{Status: publisher.StatusSucceeded, ExternalID: "pr-new"}
		newer, err = factory.Complete(context.Background(), newer.OperationKey, newer.Attempt, newerResult)
		Expect(err).NotTo(HaveOccurred())
		Expect(newer.ID).To(BeNumerically(">", first.ID))

		_, err = factory.Complete(context.Background(), first.OperationKey, first.Attempt, firstResult)
		Expect(err).NotTo(HaveOccurred())
		var linkedID int64
		Expect(dbConn.QueryRow(`
			SELECT publication_id
			FROM agent_workflow_outcomes
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, defaultTeam.ID(), int64(workflowRunID), int64(input.ID)).Scan(&linkedID)).To(Succeed())
		Expect(linkedID).To(Equal(int64(newer.ID)))
	})

	It("authorizes the exact available immutable snapshot and preserves the first merge actor", func() {
		invalid := request()
		invalid.Input.Digest = snapshot.Digest("sha256:" + strings.Repeat("b", 64))
		_, _, err := factory.Acquire(context.Background(), invalid, time.Minute)
		Expect(errors.Is(err, publisher.ErrInvalidRequest)).To(BeTrue())

		merge := request()
		merge.Mode = publisher.ModeMerge
		merge.Parameters = map[string]string{"target_branch": "main", "expected_base_sha": strings.Repeat("c", 40)}
		merge.ApprovedBy = "alice"
		merge.Approval = newApproval("alice", "approval-alice", "1", "2", time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC))
		first, execute, err := factory.Acquire(context.Background(), merge, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		Expect(first.Request.Approval).To(Equal(merge.Approval))
		merge.ApprovedBy = "bob"
		merge.Approval = newApproval("bob", "approval-bob", "3", "4", time.Date(2026, 7, 22, 12, 1, 0, 0, time.UTC))
		replay, execute, err := factory.Acquire(context.Background(), merge, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(replay.ID).To(Equal(first.ID))
		Expect(replay.Request.ApprovedBy).To(Equal("alice"))
		Expect(replay.Request.Approval).To(Equal(first.Request.Approval))
	})

	It("rejects forged or stale merge approval audit evidence", func() {
		base := request()
		base.Mode = publisher.ModeMerge
		base.Parameters = map[string]string{"target_branch": "main", "expected_base_sha": strings.Repeat("c", 40)}
		base.ApprovedBy = "alice"
		base.Approval = newApproval("alice", "approval-forgery", "5", "6", time.Date(2026, 7, 22, 13, 0, 0, 0, time.UTC))

		for _, mutate := range []func(*publisher.Request){
			func(value *publisher.Request) { value.Approval.WaitID++ },
			func(value *publisher.Request) { value.Approval.ResolvedAt = value.Approval.ResolvedAt.Add(time.Second) },
			func(value *publisher.Request) {
				value.Approval.Answer.Digest = snapshot.Digest("sha256:" + strings.Repeat("7", 64))
			},
			func(value *publisher.Request) { value.Approval.Question.ID++ },
		} {
			value := base.Clone()
			mutate(&value)
			_, _, err := factory.Acquire(context.Background(), value, time.Minute)
			Expect(errors.Is(err, publisher.ErrInvalidRequest)).To(BeTrue())
		}
	})

	It("fails closed for a wrong team, build, actor, or run-unbound snapshot", func() {
		for _, mutate := range []func(*publisher.Request){
			func(value *publisher.Request) { value.Authority.TeamID++ },
			func(value *publisher.Request) { value.Authority.TeamName = "other" },
			func(value *publisher.Request) { value.Authority.BuildID++ },
			func(value *publisher.Request) { value.Authority.WorkflowRunID = workflowRunID + 1 },
			func(value *publisher.Request) { value.Authority.Actor = "mallory" },
		} {
			value := request()
			mutate(&value)
			_, _, err := factory.Acquire(context.Background(), value, time.Minute)
			Expect(errors.Is(err, publisher.ErrInvalidRequest)).To(BeTrue())
		}

		var id int64
		digest := "sha256:" + strings.Repeat("f", 64)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ('repository-change', 1, $1, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, digest).Scan(&id)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'alice', 'publication test')
		`, id, defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())
		value := request()
		value.Input = snapshot.SnapshotRef{ID: snapshot.SnapshotID(id), Type: "repository-change/v1", Digest: snapshot.Digest(digest)}
		_, _, err = factory.Acquire(context.Background(), value, time.Minute)
		Expect(errors.Is(err, publisher.ErrInvalidRequest)).To(BeTrue())
	})

	It("authorizes a run-owned internal production that is not yet a public output binding", func() {
		var id int64
		digest := "sha256:" + strings.Repeat("9", 64)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ('repository-change', 1, $1, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, digest).Scan(&id)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_snapshot_grants (snapshot_id, team_id, granted_by, reason)
			VALUES ($1, $2, 'alice', 'publication test')
		`, id, defaultTeam.ID())
		Expect(err).NotTo(HaveOccurred())
		_, err = dbConn.Exec(`
			INSERT INTO agent_snapshot_productions
				(snapshot_id, occurrence_kind, build_id, team_id, team_name, created_by,
				 plan_id, attempt, step_kind, step_name, output_port,
				 workflow_run_id)
			VALUES ($1, 'build', $2, $3, $4, 'alice',
			        'publication-plan', '0', 'agent', 'approve', 'approved-change', $5)
		`, id, buildID, defaultTeam.ID(), defaultTeam.Name(), int64(workflowRunID))
		Expect(err).NotTo(HaveOccurred())

		value := request()
		value.Input = snapshot.SnapshotRef{
			ID: snapshot.SnapshotID(id), Type: "repository-change/v1", Digest: snapshot.Digest(digest),
		}
		publication, execute, err := factory.Acquire(context.Background(), value, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		Expect(publication.Request.Authority.WorkflowRunID).To(Equal(workflowRunID))
	})

	It("preserves cancellation and rejects unknown or mismatched completion", func() {
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := factory.Acquire(cancelled, request(), time.Minute)
		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		_, err = factory.Complete(context.Background(), "sha256:"+strings.Repeat("0", 64), 1, publisher.Result{Status: publisher.StatusSucceeded})
		Expect(errors.Is(err, publisher.ErrOperationNotFound)).To(BeTrue())
	})
})
