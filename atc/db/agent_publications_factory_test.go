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
	"github.com/concourse/concourse/agent/snapshot/contracts"
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
	var newBoundSnapshot func(typeName, portName, digestCharacter string) snapshot.SnapshotRef

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
				(team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ($1, 'repository-change', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), digest).Scan(&id)).To(Succeed())
		var err error
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
					(team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
				VALUES ($1, 'question', 1, $2, 1, 1, 'application/x-tar', 'available')
				RETURNING id
			`, defaultTeam.ID(), questionDigest).Scan(&questionID)).To(Succeed())
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
				VALUES ($1, 'human-answer', 1, $2, 1, 1, 'application/x-tar', 'available')
				RETURNING id
			`, defaultTeam.ID(), answerDigest).Scan(&answerID)).To(Succeed())
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
		newBoundSnapshot = func(typeName, portName, digestCharacter string) snapshot.SnapshotRef {
			var id int64
			digest := "sha256:" + strings.Repeat(digestCharacter, 64)
			Expect(dbConn.QueryRow(`
				INSERT INTO agent_snapshots
					(team_id, type_name, type_version, digest, byte_size, file_count,
					 representation, content_state)
				VALUES ($1, $2, 1, $3, 1, 1, 'application/x-tar', 'available')
				RETURNING id
			`, defaultTeam.ID(), typeName, digest).Scan(&id)).To(Succeed())
			_, err := dbConn.Exec(`
				INSERT INTO agent_workflow_run_snapshots
					(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
				VALUES ($1, 'output', $2, $3, now())
			`, int64(workflowRunID), portName, id)
			Expect(err).NotTo(HaveOccurred())
			return snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(id), Type: snapshot.TypeRef(typeName + "/v1"),
				Digest: snapshot.Digest(digest),
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

	It("round-trips an exact PR action through occurrence leases, evidence, and its primary workflow output", func() {
		observation := newBoundSnapshot("pull-request", "pr-observation", "1")
		candidate := newBoundSnapshot("repository-change", "pr-candidate", "2")
		validation := newBoundSnapshot("validation", "pr-validation", "3")
		impact := newBoundSnapshot("publish-impact", "pr-impact", "4")
		approval := newApproval(
			"alice", "pr-publication-approval", "5", "6",
			time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC),
		)
		action := publisher.PRAction{
			Kind: publisher.OperationPublishPRBranch,
			Branch: &publisher.BranchPublicationRequest{
				Authority: publisher.Authority{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
					BuildID: buildID, Actor: "alice",
				},
				Observation: observation, Candidate: candidate,
				Validation: validation, Impact: impact,
				Evidence: publisher.PublicationEvidence{
					Kind: publisher.EvidenceHumanWait, HumanWait: approval,
				},
				Destination: "github.example/acme/widget", ApprovalPolicyVersion: "engineering/v3",
				Locator:   publisher.PRLocator{Provider: publisher.PRProviderGitHub, Repository: "acme/widget"},
				SourceRef: "refs/heads/agent/upgrade", TargetRef: "refs/heads/main",
				ExpectedSource:    publisher.HeadExpectation{Exists: true, SHA: strings.Repeat("a", 40)},
				ExpectedTargetSHA: strings.Repeat("b", 40),
				NewSourceSHA:      strings.Repeat("c", 40),
			},
		}

		first, execute, err := factory.AcquirePR(context.Background(), action, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		Expect(first.OperationKind).To(Equal(publisher.OperationPublishPRBranch))
		Expect(first.PRAction).NotTo(BeNil())
		expected := action.Clone()
		expected.Branch.Authority.WorkflowRunID = workflowRunID
		Expect(*first.PRAction).To(Equal(expected))
		Expect(first.Request).To(Equal(publisher.Request{}),
			"a provider-native operation must never be interpreted as a legacy request")

		var (
			storedKind string
			primaryID  int64
		)
		Expect(dbConn.QueryRow(`
			SELECT publication.operation_kind, occurrence.input_snapshot_id
			FROM agent_publication_occurrences occurrence
			JOIN agent_publications publication ON publication.id=occurrence.publication_id
			WHERE occurrence.id=$1
		`, first.ID).Scan(&storedKind, &primaryID)).To(Succeed())
		Expect(storedKind).To(Equal(string(publisher.OperationPublishPRBranch)))
		Expect(primaryID).To(Equal(int64(candidate.ID)))

		rows, err := dbConn.Query(`
			SELECT role, snapshot_id
			FROM agent_publication_inputs
			WHERE publication_id=$1
		`, first.ID)
		Expect(err).NotTo(HaveOccurred())
		storedInputs := map[string]int64{}
		for rows.Next() {
			var role string
			var id int64
			Expect(rows.Scan(&role, &id)).To(Succeed())
			storedInputs[role] = id
		}
		Expect(rows.Close()).To(Succeed())
		Expect(storedInputs).To(Equal(map[string]int64{
			"observation": int64(observation.ID),
			"validation":  int64(validation.ID),
			"impact":      int64(impact.ID),
		}))
		var evidenceKind string
		var waitID int64
		Expect(dbConn.QueryRow(`
			SELECT evidence_kind, human_wait_id
			FROM agent_publication_approval_evidence
			WHERE publication_id=$1
		`, first.ID).Scan(&evidenceKind, &waitID)).To(Succeed())
		Expect(evidenceKind).To(Equal(string(publisher.EvidenceHumanWait)))
		Expect(waitID).To(Equal(int64(approval.WaitID)))

		duplicate, execute, err := factory.AcquirePR(context.Background(), action, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(duplicate).To(Equal(first))

		_, err = dbConn.Exec(`
			UPDATE agent_publications
			SET lease_until=now() - interval '1 second'
			WHERE operation_key=$1
		`, first.OperationKey)
		Expect(err).NotTo(HaveOccurred())
		reclaimed, execute, err := factory.AcquirePR(context.Background(), action, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		Expect(reclaimed.Attempt).To(Equal(2))

		_, err = factory.CompletePR(
			context.Background(), reclaimed.OperationKey, 1,
			publisher.Result{Status: publisher.StatusSucceeded},
		)
		Expect(errors.Is(err, publisher.ErrOperationConflict)).To(BeTrue())
		result := publisher.Result{
			Status: publisher.StatusSucceeded, ExternalID: "refs/heads/agent/upgrade",
			HeadSHA: strings.Repeat("c", 40), BaseSHA: strings.Repeat("b", 40),
		}
		completed, err := factory.CompletePR(
			context.Background(), reclaimed.OperationKey, reclaimed.Attempt, result,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Status).To(Equal(publisher.StatusSucceeded))
		Expect(completed.PRAction).NotTo(BeNil())
		Expect(*completed.PRAction).To(Equal(expected))

		var linkedOutputID, linkedPublicationID int64
		Expect(dbConn.QueryRow(`
			SELECT output_snapshot_id, publication_id
			FROM agent_workflow_outcomes
			WHERE team_id=$1 AND workflow_run_id=$2
		`, defaultTeam.ID(), int64(workflowRunID)).Scan(
			&linkedOutputID, &linkedPublicationID,
		)).To(Succeed())
		Expect(linkedOutputID).To(Equal(int64(candidate.ID)))
		Expect(linkedPublicationID).To(Equal(int64(completed.ID)))

		replayed, execute, err := factory.AcquirePR(context.Background(), action, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(replayed).To(Equal(completed))
		_, err = factory.Complete(
			context.Background(), reclaimed.OperationKey, reclaimed.Attempt, result,
		)
		Expect(errors.Is(err, publisher.ErrOperationNotFound)).To(BeTrue(),
			"legacy completion must not reinterpret a PR action")
	})

	It("selects and persists the exact primary output for every PR operation kind", func() {
		observation := newBoundSnapshot("pull-request", "all-pr-observation", "1")
		candidate := newBoundSnapshot("repository-change", "all-pr-candidate", "2")
		validation := newBoundSnapshot("validation", "all-pr-validation", "3")
		impact := newBoundSnapshot("publish-impact", "all-pr-impact", "4")
		responseSnapshot := newBoundSnapshot("pull-request-response", "all-pr-response", "5")
		approval := newApproval(
			"alice", "all-pr-approval", "6", "7",
			time.Date(2026, 7, 30, 8, 30, 0, 0, time.UTC),
		)
		authority := publisher.Authority{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
			BuildID: buildID, Actor: "alice",
		}
		evidence := publisher.PublicationEvidence{
			Kind: publisher.EvidenceHumanWait, HumanWait: approval,
		}
		locator := publisher.PRLocator{
			Provider: publisher.PRProviderGitHub, Repository: "acme/widget",
		}
		create := publisher.PRAction{
			Kind: publisher.OperationCreatePR,
			PullRequest: &publisher.PullRequestPublicationRequest{
				Authority: authority, Observation: observation, Candidate: candidate,
				Validation: validation, Impact: impact, Evidence: evidence,
				Destination: "github.example/acme/widget", ApprovalPolicyVersion: "engineering/v3",
				Locator: locator, SourceRef: "refs/heads/agent/upgrade",
				SourceSHA: strings.Repeat("c", 40), TargetRef: "refs/heads/main",
				TargetSHA: strings.Repeat("b", 40), Title: "Upgrade widget",
				Body: "Validated and ready for review.",
			},
		}
		status := publisher.PRAction{
			Kind: publisher.OperationPublishPRStatus,
			Status: &publisher.StatusPublicationRequest{
				Authority: authority, Observation: observation, Validation: validation,
				Evidence: evidence, Destination: "github.example/acme/widget",
				ApprovalPolicyVersion: "engineering/v3",
				Locator: publisher.PRLocator{
					Provider: publisher.PRProviderGitHub, Repository: "acme/widget", ExternalID: "42",
				},
				TargetRef: "refs/heads/main", SourceSHA: strings.Repeat("c", 40),
				State: "success", Description: "Validation passed",
				TargetURL: "https://ci.example/runs/42",
			},
		}
		response := publisher.PRAction{
			Kind: publisher.OperationRespondToReview,
			Response: &publisher.ResponsePublicationRequest{
				Authority: authority, Observation: observation, ResponseSnapshot: responseSnapshot,
				Evidence: evidence, Destination: "github.example/acme/widget",
				ApprovalPolicyVersion: "engineering/v3",
				Locator: publisher.PRLocator{
					Provider: publisher.PRProviderGitHub, Repository: "acme/widget", ExternalID: "42",
				},
				TargetRef: "refs/heads/main",
				Batch: publisher.PRReviewBatch{
					ID: "review-17", ReviewID: "17", CommitSHA: strings.Repeat("c", 40),
					Reviewer: "github-user-9", Ready: true, ThreadIDs: []string{"thread-101"},
				},
				Response: contracts.PullRequestResponseBody{
					BatchID: "review-17", Summary: "Addressed the requested changes.",
					Replies: []contracts.PullRequestThreadResponse{{
						ThreadID: "thread-101", Body: "Updated in the new revision.",
					}},
				},
			},
		}

		for _, test := range []struct {
			action  publisher.PRAction
			primary snapshot.SnapshotRef
			inputs  map[string]int64
		}{
			{
				action: create, primary: candidate,
				inputs: map[string]int64{
					"observation": int64(observation.ID),
					"validation":  int64(validation.ID),
					"impact":      int64(impact.ID),
				},
			},
			{
				action: status, primary: validation,
				inputs: map[string]int64{"observation": int64(observation.ID)},
			},
			{
				action: response, primary: responseSnapshot,
				inputs: map[string]int64{"observation": int64(observation.ID)},
			},
		} {
			publication, execute, err := factory.AcquirePR(
				context.Background(), test.action, time.Minute,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(execute).To(BeTrue())
			expectedAction := test.action.Clone()
			switch expectedAction.Kind {
			case publisher.OperationCreatePR:
				expectedAction.PullRequest.Authority.WorkflowRunID = workflowRunID
			case publisher.OperationPublishPRStatus:
				expectedAction.Status.Authority.WorkflowRunID = workflowRunID
			case publisher.OperationRespondToReview:
				expectedAction.Response.Authority.WorkflowRunID = workflowRunID
			}
			Expect(publication.PRAction).NotTo(BeNil())
			Expect(*publication.PRAction).To(Equal(expectedAction))
			var primaryID int64
			Expect(dbConn.QueryRow(`
				SELECT input_snapshot_id
				FROM agent_publication_occurrences
				WHERE id=$1
			`, publication.ID).Scan(&primaryID)).To(Succeed())
			Expect(primaryID).To(Equal(int64(test.primary.ID)))

			rows, err := dbConn.Query(`
				SELECT role, snapshot_id
				FROM agent_publication_inputs
				WHERE publication_id=$1
			`, publication.ID)
			Expect(err).NotTo(HaveOccurred())
			stored := map[string]int64{}
			for rows.Next() {
				var role string
				var id int64
				Expect(rows.Scan(&role, &id)).To(Succeed())
				stored[role] = id
			}
			Expect(rows.Close()).To(Succeed())
			Expect(stored).To(Equal(test.inputs))

			completed, err := factory.CompletePR(
				context.Background(), publication.OperationKey, publication.Attempt,
				publisher.Result{Status: publisher.StatusSucceeded},
			)
			Expect(err).NotTo(HaveOccurred())
			var linkedPublicationID int64
			Expect(dbConn.QueryRow(`
				SELECT publication_id
				FROM agent_workflow_outcomes
				WHERE team_id=$1 AND workflow_run_id=$2 AND output_snapshot_id=$3
			`, defaultTeam.ID(), int64(workflowRunID), int64(test.primary.ID)).Scan(
				&linkedPublicationID,
			)).To(Succeed())
			Expect(linkedPublicationID).To(Equal(int64(completed.ID)))
		}
	})

	It("authorizes and persists the exact accepted-review authority chain", func() {
		observation := newBoundSnapshot("pull-request", "accepted-pr-observation", "1")
		candidateChange := newBoundSnapshot("repository-change", "accepted-pr-candidate", "2")
		validation := newBoundSnapshot("validation", "accepted-pr-validation", "3")
		impact := newBoundSnapshot("publish-impact", "accepted-pr-impact", "4")

		unique := time.Now().UnixNano()
		contentHash := fmt.Sprintf("%064x", unique)
		var reviewDefinitionID, reviewVersion int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by,
				 schema_version, signature_version, definition_kind)
			SELECT 'code-review', coalesce(max(version), 0) + 1, $1,
			       'schema_version: 3', 'alice', 3, 1, 'workflow'
			FROM agent_workflow_definitions
			WHERE definition_kind='workflow' AND name='code-review'
			RETURNING id, version
		`, contentHash).Scan(&reviewDefinitionID, &reviewVersion)).To(Succeed())
		var reviewBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'succeeded', $2, 'alice')
			RETURNING id
		`, fmt.Sprintf("accepted-pr-review-%d", unique), defaultTeam.ID()).Scan(
			&reviewBuildID,
		)).To(Succeed())
		var reviewRunID snapshot.WorkflowRunID
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id,
				 workflow_name, workflow_version, schema_version, signature_version,
				 definition_content_hash, idempotency_key, parameterized_config,
				 parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id, started_at, completed_at)
			VALUES ('workflow', $1, $2, $3, 'code-review', $4, 3, 1,
			        $5, $6, '{}', $7, 'manual', '', 'alice', 'succeeded',
			        $8, now(), now())
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), reviewDefinitionID, reviewVersion,
			contentHash, fmt.Sprintf("accepted-pr-review-%d", unique),
			fmt.Sprintf("%064x", unique+1), reviewBuildID).Scan(&reviewRunID)).To(Succeed())

		var reviewCandidateID, reviewID int64
		reviewCandidateDigest := "sha256:" + fmt.Sprintf("%064x", unique+2)
		reviewDigest := "sha256:" + fmt.Sprintf("%064x", unique+3)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count,
				 representation, content_state)
			VALUES ($1, 'repository', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), reviewCandidateDigest).Scan(&reviewCandidateID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count,
				 representation, content_state)
			VALUES ($1, 'review', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), reviewDigest).Scan(&reviewID)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'input', 'after', $2, now()),
			       ($1, 'output', 'review', $3, now())
		`, int64(reviewRunID), reviewCandidateID, reviewID)
		Expect(err).NotTo(HaveOccurred())
		acceptedAt := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, human_modified, intervention_count, labels,
				 actor, revision, audited_at)
			VALUES ($1, $2, $3, 'accepted', 'not_requested',
			        false, 0, '[]'::jsonb, 'alice', 3, $4)
		`, defaultTeam.ID(), int64(reviewRunID), reviewID, acceptedAt)
		Expect(err).NotTo(HaveOccurred())

		evidence := publisher.AcceptedReviewEvidence{
			Review: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(reviewID), Type: "review/v1",
				Digest: snapshot.Digest(reviewDigest),
			},
			Candidate: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(reviewCandidateID), Type: "repository/v1",
				Digest: snapshot.Digest(reviewCandidateDigest),
			},
			Validation: validation, ReviewWorkflowRunID: reviewRunID,
			OutcomeRevision: 3, AcceptedBy: "alice", AcceptedAt: acceptedAt,
		}
		action := publisher.PRAction{
			Kind: publisher.OperationPublishPRBranch,
			Branch: &publisher.BranchPublicationRequest{
				Authority: publisher.Authority{
					TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
					BuildID: buildID, Actor: "alice",
				},
				Observation: observation, Candidate: candidateChange,
				Validation: validation, Impact: impact,
				Evidence: publisher.PublicationEvidence{
					Kind: publisher.EvidenceAcceptedReview, AcceptedReview: &evidence,
				},
				Destination: "github.example/acme/widget", ApprovalPolicyVersion: "engineering/v3",
				Locator:   publisher.PRLocator{Provider: publisher.PRProviderGitHub, Repository: "acme/widget"},
				SourceRef: "refs/heads/agent/upgrade", TargetRef: "refs/heads/main",
				ExpectedSource:    publisher.HeadExpectation{Exists: true, SHA: strings.Repeat("a", 40)},
				ExpectedTargetSHA: strings.Repeat("b", 40), NewSourceSHA: strings.Repeat("c", 40),
			},
		}

		publication, execute, err := factory.AcquirePR(context.Background(), action, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeTrue())
		var storedReviewID, storedRunID, storedRevision int64
		Expect(dbConn.QueryRow(`
			SELECT review_snapshot_id, review_workflow_run_id, outcome_revision
			FROM agent_publication_approval_evidence
			WHERE publication_id=$1
		`, publication.ID).Scan(
			&storedReviewID, &storedRunID, &storedRevision,
		)).To(Succeed())
		Expect(storedReviewID).To(Equal(reviewID))
		Expect(storedRunID).To(Equal(int64(reviewRunID)))
		Expect(storedRevision).To(Equal(int64(3)))

		var secondBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'started', $2, 'bob')
			RETURNING id
		`, fmt.Sprintf("accepted-pr-replay-%d", unique), defaultTeam.ID()).Scan(
			&secondBuildID,
		)).To(Succeed())
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
			fmt.Sprintf("accepted-pr-replay-%d", unique), strings.Repeat("d", 64),
			fmt.Sprintf("accepted-pr-replay-%d", unique), strings.Repeat("e", 64),
			secondBuildID).Scan(&secondRunID)).To(Succeed())
		_, err = dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'output', 'observation', $2, now()),
			       ($1, 'output', 'candidate', $3, now()),
			       ($1, 'output', 'validation', $4, now()),
			       ($1, 'output', 'impact', $5, now())
		`, int64(secondRunID), int64(observation.ID), int64(candidateChange.ID),
			int64(validation.ID), int64(impact.ID))
		Expect(err).NotTo(HaveOccurred())
		secondAction := action.Clone()
		secondAction.Branch.Authority = publisher.Authority{
			TeamID: defaultTeam.ID(), TeamName: defaultTeam.Name(),
			BuildID: secondBuildID, Actor: "bob",
		}
		second, execute, err := factory.AcquirePR(
			context.Background(), secondAction, time.Minute,
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(execute).To(BeFalse())
		Expect(second.ID).NotTo(Equal(publication.ID))
		Expect(second.OperationKey).To(Equal(publication.OperationKey))
		Expect(second.PRAction.Branch.Authority.WorkflowRunID).To(Equal(secondRunID))
		Expect(second.PRAction.Branch.Authority.Actor).To(Equal("bob"))

		completed, err := factory.CompletePR(
			context.Background(), publication.OperationKey, publication.Attempt,
			publisher.Result{Status: publisher.StatusSucceeded, HeadSHA: strings.Repeat("c", 40)},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(completed.Status).To(Equal(publisher.StatusSucceeded))
		var secondOutcomePublicationID int64
		Expect(dbConn.QueryRow(`
			SELECT publication_id
			FROM agent_workflow_outcomes
			WHERE team_id=$1 AND workflow_run_id=$2 AND output_snapshot_id=$3
		`, defaultTeam.ID(), int64(secondRunID), int64(candidateChange.ID)).Scan(
			&secondOutcomePublicationID,
		)).To(Succeed())
		Expect(secondOutcomePublicationID).To(Equal(int64(second.ID)))

		forged := action.Clone()
		forged.Branch.Evidence.AcceptedReview.OutcomeRevision++
		_, _, err = factory.AcquirePR(context.Background(), forged, time.Minute)
		Expect(errors.Is(err, publisher.ErrInvalidRequest)).To(BeTrue())
	})

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
				(team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ($1, 'repository-change', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), digest).Scan(&id)).To(Succeed())
		var err error
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
				(team_id, type_name, type_version, digest, byte_size, file_count, representation, content_state)
			VALUES ($1, 'repository-change', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), digest).Scan(&id)).To(Succeed())
		var err error
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

	It("resolves accepted review authority from one exact frozen code-review run", func() {
		unique := time.Now().UnixNano()
		contentHash := fmt.Sprintf("%064x", unique)
		var reviewDefinitionID, reviewVersion int
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_definitions
				(name, version, content_hash, definition, created_by,
				 schema_version, signature_version, definition_kind)
			SELECT 'code-review', coalesce(max(version), 0) + 1, $1,
			       'schema_version: 3', 'alice', 3, 1, 'workflow'
			FROM agent_workflow_definitions
			WHERE definition_kind = 'workflow' AND name = 'code-review'
			RETURNING id, version
		`, contentHash).Scan(&reviewDefinitionID, &reviewVersion)).To(Succeed())

		var reviewBuildID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO builds (name, status, team_id, created_by)
			VALUES ($1, 'succeeded', $2, 'alice') RETURNING id
		`, "review-evidence-"+fmt.Sprint(unique), defaultTeam.ID()).Scan(&reviewBuildID)).To(Succeed())
		var reviewRunID int64
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_workflow_runs
				(definition_kind, team_id, team_name, workflow_definition_id,
				 workflow_name, workflow_version, schema_version, signature_version,
				 definition_content_hash, idempotency_key, parameterized_config,
				 parameterized_config_hash, origin_kind, origin_reference,
				 created_by, status, planned_build_id, started_at, completed_at)
			VALUES ('workflow', $1, $2, $3, 'code-review', $4, 3, 1,
			        $5, $6, '{}', $7, 'manual', '', 'alice', 'succeeded',
			        $8, now(), now())
			RETURNING id
		`, defaultTeam.ID(), defaultTeam.Name(), reviewDefinitionID, reviewVersion,
			contentHash, "review-evidence-"+fmt.Sprint(unique),
			fmt.Sprintf("%064x", unique+1), reviewBuildID,
		).Scan(&reviewRunID)).To(Succeed())

		var candidateID, reviewID int64
		candidateDigest := "sha256:" + fmt.Sprintf("%064x", unique+2)
		reviewDigest := "sha256:" + fmt.Sprintf("%064x", unique+3)
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count,
				 representation, content_state)
			VALUES ($1, 'repository', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), candidateDigest).Scan(&candidateID)).To(Succeed())
		Expect(dbConn.QueryRow(`
			INSERT INTO agent_snapshots
				(team_id, type_name, type_version, digest, byte_size, file_count,
				 representation, content_state)
			VALUES ($1, 'review', 1, $2, 1, 1, 'application/x-tar', 'available')
			RETURNING id
		`, defaultTeam.ID(), reviewDigest).Scan(&reviewID)).To(Succeed())
		_, err := dbConn.Exec(`
			INSERT INTO agent_workflow_run_snapshots
				(workflow_run_id, direction, port_name, snapshot_id, promoted_at)
			VALUES ($1, 'input', 'after', $2, now()),
			       ($1, 'output', 'review', $3, now())
		`, reviewRunID, candidateID, reviewID)
		Expect(err).NotTo(HaveOccurred())

		evidence, found, err := factory.ResolveReviewRunEvidence(
			context.Background(), defaultTeam.ID(), snapshot.WorkflowRunID(reviewRunID),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeTrue())
		Expect(evidence).To(Equal(publisher.ReviewRunEvidence{
			TeamID:                defaultTeam.ID(),
			WorkflowRunID:         snapshot.WorkflowRunID(reviewRunID),
			WorkflowDefinitionID:  reviewDefinitionID,
			WorkflowName:          "code-review",
			WorkflowVersion:       reviewVersion,
			SchemaVersion:         3,
			DefinitionContentHash: contentHash,
			CandidateInput:        "after",
			Candidate: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(candidateID), Type: "repository/v1",
				Digest: snapshot.Digest(candidateDigest),
			},
			ReviewOutput: "review",
			Review: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(reviewID), Type: "review/v1",
				Digest: snapshot.Digest(reviewDigest),
			},
		}))

		_, found, err = factory.ResolveReviewRunEvidence(
			context.Background(), defaultTeam.ID()+1, snapshot.WorkflowRunID(reviewRunID),
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(found).To(BeFalse())
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
