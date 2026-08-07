package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflowwait"
)

type AgentPublicationsFactory interface {
	publisher.Store
	publisher.ReviewRunEvidenceResolver
}

func NewAgentPublicationsFactory(conn DbConn) AgentPublicationsFactory {
	return &agentPublicationsFactory{conn: conn}
}

type agentPublicationsFactory struct {
	conn DbConn
}

func (factory *agentPublicationsFactory) ResolveReviewRunEvidence(
	ctx context.Context,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
) (publisher.ReviewRunEvidence, bool, error) {
	if ctx == nil || teamID <= 0 || workflowRunID.Validate() != nil {
		return publisher.ReviewRunEvidence{}, false, fmt.Errorf(
			"db: review run evidence requires context, team, and workflow run",
		)
	}
	var (
		evidence             publisher.ReviewRunEvidence
		runID                int64
		candidateID          int64
		candidateTypeName    string
		candidateTypeVersion int
		candidateDigest      string
		reviewID             int64
		reviewTypeName       string
		reviewTypeVersion    int
		reviewDigest         string
	)
	err := factory.conn.QueryRowContext(ctx, `
		SELECT run.team_id, run.id, run.workflow_definition_id,
		       run.workflow_name, run.workflow_version, run.schema_version,
		       run.definition_content_hash,
		       candidate.port_name, candidate_snapshot.id,
		       candidate_snapshot.type_name, candidate_snapshot.type_version,
		       candidate_snapshot.digest,
		       review.port_name, review_snapshot.id,
		       review_snapshot.type_name, review_snapshot.type_version,
		       review_snapshot.digest
		FROM agent_workflow_runs run
		JOIN agent_workflow_run_snapshots candidate
		  ON candidate.workflow_run_id=run.id
		 AND candidate.direction='input'
		 AND candidate.port_name='after'
		 AND candidate.promoted_at IS NOT NULL
		JOIN agent_snapshots candidate_snapshot
		  ON candidate_snapshot.id=candidate.snapshot_id
		 AND candidate_snapshot.team_id=run.team_id
		 AND candidate_snapshot.type_name='repository'
		 AND candidate_snapshot.type_version=1
		JOIN agent_workflow_run_snapshots review
		  ON review.workflow_run_id=run.id
		 AND review.direction='output'
		 AND review.port_name='review'
		 AND review.promoted_at IS NOT NULL
		JOIN agent_snapshots review_snapshot
		  ON review_snapshot.id=review.snapshot_id
		 AND review_snapshot.team_id=run.team_id
		 AND review_snapshot.type_name='review'
		 AND review_snapshot.type_version=1
		WHERE run.id=$1 AND run.team_id=$2
		  AND run.definition_kind='workflow'
		  AND run.workflow_name='code-review'
		  AND run.schema_version=3
		  AND run.status='succeeded'
	`, int64(workflowRunID), teamID).Scan(
		&evidence.TeamID, &runID, &evidence.WorkflowDefinitionID,
		&evidence.WorkflowName, &evidence.WorkflowVersion, &evidence.SchemaVersion,
		&evidence.DefinitionContentHash,
		&evidence.CandidateInput, &candidateID,
		&candidateTypeName, &candidateTypeVersion, &candidateDigest,
		&evidence.ReviewOutput, &reviewID,
		&reviewTypeName, &reviewTypeVersion, &reviewDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return publisher.ReviewRunEvidence{}, false, nil
	}
	if err != nil {
		return publisher.ReviewRunEvidence{}, false, err
	}
	candidateType, err := joinSnapshotType(candidateTypeName, candidateTypeVersion)
	if err != nil {
		return publisher.ReviewRunEvidence{}, false, err
	}
	reviewType, err := joinSnapshotType(reviewTypeName, reviewTypeVersion)
	if err != nil {
		return publisher.ReviewRunEvidence{}, false, err
	}
	evidence.WorkflowRunID = snapshot.WorkflowRunID(runID)
	evidence.Candidate = snapshot.SnapshotRef{
		ID: snapshot.SnapshotID(candidateID), Type: candidateType,
		Digest: snapshot.Digest(candidateDigest),
	}
	evidence.Review = snapshot.SnapshotRef{
		ID: snapshot.SnapshotID(reviewID), Type: reviewType,
		Digest: snapshot.Digest(reviewDigest),
	}
	return evidence, true, nil
}

const agentPublicationColumns = `
	p.id, occurrence.id, p.operation_key, p.operation_kind, p.operation_payload, p.publisher,
	s.id, s.type_name, s.type_version, s.digest,
	p.destination, p.mode, p.parameters, p.approval_policy_version, occurrence.approved_by,
	occurrence.approval_wait_id, approval_question.id, approval_question.type_name, approval_question.type_version,
	approval_question.digest, approval_answer.id, approval_answer.type_name, approval_answer.type_version,
	approval_answer.digest, occurrence.approval_resolved_at,
	occurrence.team_id, occurrence.team_name, occurrence.workflow_run_id, occurrence.build_id, occurrence.actor,
	p.status, p.attempt, p.lease_until, p.result,
	occurrence.created_at, GREATEST(occurrence.updated_at, p.updated_at)`

const agentPublicationFrom = `
	FROM agent_publications p
	JOIN agent_publication_occurrences occurrence ON occurrence.publication_id = p.id
	JOIN agent_snapshots s ON s.id = p.input_snapshot_id
	LEFT JOIN agent_snapshots approval_question ON approval_question.id = occurrence.approval_question_snapshot_id
	LEFT JOIN agent_snapshots approval_answer ON approval_answer.id = occurrence.approval_answer_snapshot_id`

type agentPublicationRecord struct {
	operationID int64
	publication publisher.Publication
}

func (factory *agentPublicationsFactory) Acquire(
	ctx context.Context,
	request publisher.Request,
	lease time.Duration,
) (publisher.Publication, bool, error) {
	if ctx == nil {
		return publisher.Publication{}, false, fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, false, err
	}
	request = request.Clone()
	key, err := request.OperationKey()
	if err != nil {
		return publisher.Publication{}, false, err
	}
	if lease <= 0 || lease > 24*time.Hour {
		return publisher.Publication{}, false, fmt.Errorf("%w: lease must be within 0-24h", publisher.ErrInvalidRequest)
	}
	if request.Parameters == nil {
		request.Parameters = map[string]string{}
	}
	parameters, err := json.Marshal(request.Parameters)
	if err != nil {
		return publisher.Publication{}, false, fmt.Errorf("%w: encode parameters", publisher.ErrInvalidRequest)
	}

	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	defer Rollback(tx)
	workflowRunID, err := authorizePublicationSnapshot(ctx, tx, request)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	request.Authority.WorkflowRunID = workflowRunID
	if err := request.ValidatePersisted(); err != nil {
		return publisher.Publication{}, false, err
	}
	if err := authorizePublicationApproval(ctx, tx, request); err != nil {
		return publisher.Publication{}, false, err
	}
	var approvalWaitID, approvalQuestionID, approvalAnswerID any
	var approvalResolvedAt any
	if request.Approval != nil {
		approvalWaitID = int64(request.Approval.WaitID)
		approvalQuestionID = int64(request.Approval.Question.ID)
		approvalAnswerID = int64(request.Approval.Answer.ID)
		approvalResolvedAt = request.Approval.ResolvedAt.UTC()
	}
	var operationID, reservedOccurrenceID int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_publications
			(operation_key, team_id, team_name, workflow_run_id, build_id, actor,
			 input_snapshot_id, publisher, destination, mode, parameters,
			 approval_policy_version, approved_by, approval_wait_id,
			 approval_question_snapshot_id, approval_answer_snapshot_id, approval_resolved_at,
			 status, attempt, lease_until, result, lease_owner_occurrence_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        $14, $15, $16, $17, 'pending', 1,
		        now() + ($18::double precision * interval '1 second'), '{}'::jsonb,
		        nextval('agent_publication_occurrences_id_seq'))
		ON CONFLICT (operation_key) DO NOTHING
		RETURNING id, lease_owner_occurrence_id
	`, key, request.Authority.TeamID, request.Authority.TeamName, int64(request.Authority.WorkflowRunID),
		request.Authority.BuildID, request.Authority.Actor, int64(request.Input.ID), request.Publisher.String(),
		request.Destination, string(request.Mode), parameters, request.ApprovalPolicyVersion,
		nullableNonblank(request.ApprovedBy), approvalWaitID, approvalQuestionID, approvalAnswerID,
		approvalResolvedAt, lease.Seconds()).Scan(&operationID, &reservedOccurrenceID)
	inserted := true
	if errors.Is(err, sql.ErrNoRows) {
		inserted = false
		err = nil
	}
	if err != nil {
		return publisher.Publication{}, false, err
	}
	if !inserted {
		err = tx.QueryRowContext(ctx, `
			SELECT id
			FROM agent_publications
			WHERE operation_key = $1
			FOR UPDATE
		`, key).Scan(&operationID)
	} else {
		err = tx.QueryRowContext(ctx, `
			SELECT id
			FROM agent_publications
			WHERE id = $1
			FOR UPDATE
		`, operationID).Scan(&operationID)
	}
	if err != nil {
		return publisher.Publication{}, false, err
	}
	occurrenceID, err := ensureAgentPublicationOccurrence(
		ctx, tx, operationID, reservedOccurrenceID, inserted,
		agentPublicationOccurrence{
			authority:  request.Authority,
			input:      request.Input,
			approvedBy: request.ApprovedBy,
			approval:   request.Approval,
		},
	)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	record, found, err := getAgentPublicationOccurrence(ctx, tx, occurrenceID, false)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	if !found {
		return publisher.Publication{}, false, fmt.Errorf("db: publication occurrence disappeared after acquire")
	}
	publication := record.publication
	// Compared on operation identity, not on the whole authority: the stored
	// request is rehydrated from agent_publications, which carries no plan
	// position because one operation is deliberately shared across plan
	// positions. See publisher.Authority.OperationIdentity.
	if storedKey, keyErr := publication.Request.OperationKey(); keyErr != nil || storedKey != key ||
		publication.Request.Authority.OperationIdentity() != request.Authority.OperationIdentity() {
		return publisher.Publication{}, false, publisher.ErrOperationConflict
	}
	if inserted {
		if err := tx.Commit(); err != nil {
			return publisher.Publication{}, false, err
		}
		return publication, true, nil
	}
	if publicationStatusTerminal(publication.Status) {
		if err := linkSucceededPublicationOutcome(ctx, tx, publication); err != nil {
			return publisher.Publication{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return publisher.Publication{}, false, err
		}
		return publication, false, nil
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	if now.Before(publication.LeaseUntil) {
		if err := tx.Commit(); err != nil {
			return publisher.Publication{}, false, err
		}
		return publication, false, nil
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE agent_publications
		SET attempt = attempt + 1,
		    lease_until = $2::timestamptz + ($3::double precision * interval '1 second'),
		    lease_owner_occurrence_id = $5,
		    updated_at = $2::timestamptz
		WHERE operation_key = $1 AND status = 'pending' AND attempt = $4
	`, key, now, lease.Seconds(), publication.Attempt, occurrenceID)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		if err == nil {
			err = publisher.ErrOperationConflict
		}
		return publisher.Publication{}, false, err
	}
	record, found, err = getAgentPublicationOccurrence(ctx, tx, occurrenceID, false)
	if err != nil {
		return publisher.Publication{}, false, err
	}
	if !found {
		return publisher.Publication{}, false, fmt.Errorf("db: publication occurrence disappeared after lease reclaim")
	}
	publication = record.publication
	if err := tx.Commit(); err != nil {
		return publisher.Publication{}, false, err
	}
	return publication, true, nil
}

type agentPublicationOccurrence struct {
	authority  publisher.Authority
	input      snapshot.SnapshotRef
	approvedBy string
	approval   *publisher.ApprovalEvidence
}

func ensureAgentPublicationOccurrence(
	ctx context.Context,
	tx Tx,
	operationID int64,
	reservedOccurrenceID int64,
	operationInserted bool,
	occurrence agentPublicationOccurrence,
) (int64, error) {
	var approvalWaitID, approvalQuestionID, approvalAnswerID any
	var approvalResolvedAt any
	if occurrence.approval != nil {
		approvalWaitID = int64(occurrence.approval.WaitID)
		approvalQuestionID = int64(occurrence.approval.Question.ID)
		approvalAnswerID = int64(occurrence.approval.Answer.ID)
		approvalResolvedAt = occurrence.approval.ResolvedAt.UTC()
	}
	// plan_id is the only thing that lets the durable node-occurrence
	// projection join a publish node to the occurrence it produced: this row's
	// own key is (publication_id, workflow_run_id, build_id) and carries no
	// plan identity, so without it every publish node in every run's
	// projection would freeze as pending. It stays NULL when the authority
	// carries no plan position, matching the pre-projection rows migration
	// 1773106157 deliberately left alone.
	planID := nullableNonblank(occurrence.authority.PlanID)
	var occurrenceID int64
	if operationInserted {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO agent_publication_occurrences
				(id, publication_id, team_id, team_name, workflow_run_id, build_id, actor,
				 input_snapshot_id, approved_by, approval_wait_id,
				 approval_question_snapshot_id, approval_answer_snapshot_id,
				 approval_resolved_at, plan_id, status)
			SELECT $1, publication.id, $3, $4, $5, $6, $7,
			       $8, $9, $10, $11, $12, $13, $14, publication.status
			FROM agent_publications publication
			WHERE publication.id = $2
			RETURNING id
		`, reservedOccurrenceID, operationID, occurrence.authority.TeamID, occurrence.authority.TeamName,
			int64(occurrence.authority.WorkflowRunID), occurrence.authority.BuildID, occurrence.authority.Actor,
			int64(occurrence.input.ID), nullableNonblank(occurrence.approvedBy), approvalWaitID,
			approvalQuestionID, approvalAnswerID, approvalResolvedAt, planID,
		).Scan(&occurrenceID)
		return occurrenceID, err
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_publication_occurrences
			(publication_id, team_id, team_name, workflow_run_id, build_id, actor,
			 input_snapshot_id, approved_by, approval_wait_id,
			 approval_question_snapshot_id, approval_answer_snapshot_id,
			 approval_resolved_at, plan_id, status)
		SELECT publication.id, $2, $3, $4, $5, $6,
		       $7, $8, $9, $10, $11, $12, $13, publication.status
		FROM agent_publications publication
		WHERE publication.id = $1
		ON CONFLICT (publication_id, workflow_run_id, build_id) DO NOTHING
		RETURNING id
	`, operationID, occurrence.authority.TeamID, occurrence.authority.TeamName,
		int64(occurrence.authority.WorkflowRunID), occurrence.authority.BuildID, occurrence.authority.Actor,
		int64(occurrence.input.ID), nullableNonblank(occurrence.approvedBy), approvalWaitID,
		approvalQuestionID, approvalAnswerID, approvalResolvedAt, planID,
	).Scan(&occurrenceID)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `
			SELECT id
			FROM agent_publication_occurrences
			WHERE publication_id = $1 AND workflow_run_id = $2 AND build_id = $3
			FOR UPDATE
		`, operationID, int64(occurrence.authority.WorkflowRunID), occurrence.authority.BuildID).Scan(&occurrenceID)
	}
	return occurrenceID, err
}

func (factory *agentPublicationsFactory) Complete(
	ctx context.Context,
	operationKey string,
	attempt int,
	result publisher.Result,
) (publisher.Publication, error) {
	if ctx == nil {
		return publisher.Publication{}, fmt.Errorf("%w: context is required", publisher.ErrInvalidResult)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, err
	}
	if attempt <= 0 || result.Validate() != nil {
		return publisher.Publication{}, publisher.ErrInvalidResult
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return publisher.Publication{}, publisher.ErrInvalidResult
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return publisher.Publication{}, err
	}
	defer Rollback(tx)
	record, found, err := getAgentPublication(ctx, tx, operationKey, true)
	if err != nil {
		return publisher.Publication{}, err
	}
	if !found {
		return publisher.Publication{}, publisher.ErrOperationNotFound
	}
	publication := record.publication
	if publicationStatusTerminal(publication.Status) {
		if publication.Attempt != attempt || publication.Result != result {
			return publisher.Publication{}, publisher.ErrOperationConflict
		}
		if err := linkSucceededPublicationOccurrences(ctx, tx, record.operationID); err != nil {
			return publisher.Publication{}, err
		}
		if err := tx.Commit(); err != nil {
			return publisher.Publication{}, err
		}
		return publication, nil
	}
	if publication.Attempt != attempt {
		return publisher.Publication{}, publisher.ErrOperationConflict
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return publisher.Publication{}, err
	}
	updated, err := tx.ExecContext(ctx, `
		UPDATE agent_publications
		SET status = $2, result = $3, lease_until = NULL, updated_at = $4
		WHERE operation_key = $1 AND status = 'pending' AND attempt = $5
	`, operationKey, string(result.Status), payload, now, attempt)
	if err != nil {
		return publisher.Publication{}, err
	}
	count, err := updated.RowsAffected()
	if err != nil || count != 1 {
		if err == nil {
			err = publisher.ErrOperationConflict
		}
		return publisher.Publication{}, err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_publication_occurrences
		SET updated_at = $2
		WHERE publication_id = $1
	`, record.operationID, now)
	if err != nil {
		return publisher.Publication{}, err
	}
	record, found, err = getAgentPublication(ctx, tx, operationKey, false)
	if err != nil {
		return publisher.Publication{}, err
	}
	if !found {
		return publisher.Publication{}, fmt.Errorf("db: publication disappeared after completion")
	}
	publication = record.publication
	if err := linkSucceededPublicationOccurrences(ctx, tx, record.operationID); err != nil {
		return publisher.Publication{}, err
	}
	if err := tx.Commit(); err != nil {
		return publisher.Publication{}, err
	}
	return publication, nil
}

func linkSucceededPublicationOccurrences(ctx context.Context, tx Tx, operationID int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT `+agentPublicationColumns+agentPublicationFrom+`
		WHERE p.id = $1
		ORDER BY occurrence.team_id, occurrence.workflow_run_id,
		         occurrence.input_snapshot_id, occurrence.id
	`, operationID)
	if err != nil {
		return err
	}
	publications := make([]publisher.Publication, 0)
	for rows.Next() {
		record, err := scanAgentPublication(rows)
		if err != nil {
			Close(rows)
			return err
		}
		publications = append(publications, record.publication)
	}
	if err := rows.Err(); err != nil {
		Close(rows)
		return err
	}
	Close(rows)
	for _, publication := range publications {
		if err := linkSucceededPublicationOutcome(ctx, tx, publication); err != nil {
			return err
		}
	}
	return nil
}

func linkSucceededPublicationOutcome(ctx context.Context, tx Tx, publication publisher.Publication) error {
	if publication.Status != publisher.StatusSucceeded {
		return nil
	}
	if publication.ID <= 0 || publication.Result.Status != publisher.StatusSucceeded ||
		publication.Result.Validate() != nil {
		return fmt.Errorf("db: succeeded publication is invalid")
	}
	authority, output, disposition, err := publicationOutcomeIdentity(publication)
	if err != nil {
		return err
	}
	teamID := authority.TeamID
	runID := authority.WorkflowRunID
	outputID := output.ID
	if err := lockWorkflowOutcome(ctx, tx, teamID, runID, outputID); err != nil {
		return err
	}
	existing, found, err := getWorkflowOutcomeRaw(ctx, tx, teamID, runID, outputID, true)
	if err != nil {
		return err
	}
	if found && existing.PublicationState == workflowoutcomes.PublicationPublished &&
		existing.PublicationID != nil {
		if *existing.PublicationID == publication.ID {
			return nil
		}
		// One output can be published through more than one semantic provider
		// operation. Occurrence IDs provide a stable durable order so replaying
		// an older completion cannot roll authoritative evidence backwards.
		if *existing.PublicationID > publication.ID {
			return nil
		}
	}
	if found {
		_, err = tx.ExecContext(ctx, `
			UPDATE agent_workflow_outcomes
			SET publication_state = 'published',
			    publication_id = $4,
			    publication_status = 'succeeded',
			    actor = $5,
			    revision = revision + 1,
			    audited_at = now()
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
		`, teamID, int64(runID), int64(outputID), int64(publication.ID),
			authority.Actor)
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_outcomes
			(team_id, workflow_run_id, output_snapshot_id, disposition,
			 publication_state, publication_id, publication_status,
			 human_modified, intervention_count, labels, actor, revision, audited_at)
		VALUES ($1, $2, $3, $4, 'published', $5, 'succeeded',
		        false, 0, '[]'::jsonb, $6, 1, now())
	`, teamID, int64(runID), int64(outputID), disposition, int64(publication.ID),
		authority.Actor)
	return err
}

func publicationOutcomeIdentity(
	publication publisher.Publication,
) (publisher.Authority, snapshot.SnapshotRef, workflowoutcomes.Disposition, error) {
	if publication.Request.ValidatePersisted() != nil {
		return publisher.Authority{}, snapshot.SnapshotRef{}, "",
			fmt.Errorf("db: succeeded publication is invalid")
	}
	disposition := workflowoutcomes.DispositionAccepted
	if publication.Request.Mode == publisher.ModeMerge {
		disposition = workflowoutcomes.DispositionMerged
	}
	return publication.Request.Authority, publication.Request.Input, disposition, nil
}

func (factory *agentPublicationsFactory) Get(
	ctx context.Context,
	operationKey string,
) (publisher.Publication, bool, error) {
	if ctx == nil {
		return publisher.Publication{}, false, fmt.Errorf("%w: context is required", publisher.ErrInvalidRequest)
	}
	if err := ctx.Err(); err != nil {
		return publisher.Publication{}, false, err
	}
	record, found, err := getAgentPublication(ctx, factory.conn, operationKey, false)
	return record.publication, found, err
}

func authorizePublicationSnapshot(
	ctx context.Context,
	tx Tx,
	request publisher.Request,
) (snapshot.WorkflowRunID, error) {
	return authorizePublicationSnapshotRef(ctx, tx, request.Authority, request.Input, false)
}

func authorizePublicationSnapshotRef(
	ctx context.Context,
	tx Tx,
	authority publisher.Authority,
	input snapshot.SnapshotRef,
	requireWorkflowOutput bool,
) (snapshot.WorkflowRunID, error) {
	var typeName string
	var typeVersion int
	var digest string
	var contentState string
	var workflowRunID snapshot.WorkflowRunID
	err := tx.QueryRowContext(ctx, `
		SELECT workflow_run.id, input.type_name, input.type_version, input.digest, input.content_state
		FROM agent_workflow_runs workflow_run
		JOIN builds build
		  ON build.id = workflow_run.planned_build_id
		 AND build.team_id = workflow_run.team_id
		JOIN agent_snapshots input ON input.id = $1 AND input.team_id = workflow_run.team_id
		WHERE workflow_run.team_id = $2
		  AND workflow_run.team_name = $3
		  AND workflow_run.planned_build_id = $4
		  AND COALESCE(NULLIF(btrim(build.created_by), ''), 'concourse') = $5
		  AND (
			EXISTS (
				SELECT 1
				FROM agent_workflow_run_snapshots binding
				WHERE binding.workflow_run_id = workflow_run.id
				  AND binding.snapshot_id = input.id
				  AND ($6::boolean = false OR binding.direction = 'output')
			)
			OR EXISTS (
				SELECT 1
				FROM agent_snapshot_productions production
				WHERE production.occurrence_kind = 'build'
				  AND production.workflow_run_id = workflow_run.id
				  AND production.build_id = build.id
				  AND production.team_id = workflow_run.team_id
				  AND production.snapshot_id = input.id
			)
		  )
		FOR SHARE OF workflow_run, build, input
	`, int64(input.ID), authority.TeamID, authority.TeamName,
		authority.BuildID, authority.Actor, requireWorkflowOutput,
	).Scan(&workflowRunID, &typeName, &typeVersion, &digest, &contentState)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, publisher.ErrInvalidRequest
	}
	if err != nil {
		return 0, err
	}
	typ, err := joinSnapshotType(typeName, typeVersion)
	if err != nil || typ != input.Type || digest != input.Digest.String() ||
		contentState != string(snapshot.ContentStateAvailable) {
		return 0, publisher.ErrInvalidRequest
	}
	if authority.WorkflowRunID != 0 && authority.WorkflowRunID != workflowRunID {
		return 0, publisher.ErrInvalidRequest
	}
	return workflowRunID, nil
}

func authorizePublicationApproval(ctx context.Context, tx Tx, request publisher.Request) error {
	if request.Mode != publisher.ModeMerge {
		if request.Approval != nil || request.ApprovedBy != "" {
			return publisher.ErrInvalidRequest
		}
		return nil
	}
	if request.Approval == nil {
		return publisher.ErrInvalidRequest
	}
	if request.ApprovedBy != request.Approval.ResolvedBy {
		return publisher.ErrInvalidRequest
	}
	return authorizePublicationHumanWait(ctx, tx, request.Authority, *request.Approval)
}

func authorizePublicationHumanWait(
	ctx context.Context,
	tx Tx,
	authority publisher.Authority,
	evidence publisher.ApprovalEvidence,
) error {
	var questionTypeName, answerTypeName, questionDigest, answerDigest string
	var questionTypeVersion, answerTypeVersion int
	err := tx.QueryRowContext(ctx, `
		SELECT question.type_name, question.type_version, question.digest,
		       answer.type_name, answer.type_version, answer.digest
		FROM agent_workflow_waits wait
		JOIN agent_snapshots question ON question.id = wait.question_snapshot_id AND question.team_id = wait.team_id
		JOIN agent_snapshots answer ON answer.id = wait.answer_snapshot_id AND answer.team_id = wait.team_id
		WHERE wait.id = $1
		  AND wait.team_id = $2
		  AND wait.workflow_run_id = $3
		  AND wait.build_id_evidence = $4
		  AND wait.status = 'resolved'
		  AND wait.resolution_source = 'human'
		  AND wait.resolved_by = $5
		  AND wait.resolved_at = $6
		  AND wait.question_snapshot_id = $7
		  AND wait.answer_snapshot_id = $8
		  AND question.content_state = 'available'
		  AND answer.content_state = 'available'
		FOR SHARE OF wait, question, answer
	`, int64(evidence.WaitID), authority.TeamID, int64(authority.WorkflowRunID),
		authority.BuildID, evidence.ResolvedBy, evidence.ResolvedAt.UTC(),
		int64(evidence.Question.ID), int64(evidence.Answer.ID),
	).Scan(&questionTypeName, &questionTypeVersion, &questionDigest, &answerTypeName, &answerTypeVersion, &answerDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return publisher.ErrInvalidRequest
	}
	if err != nil {
		return err
	}
	questionType, questionErr := joinSnapshotType(questionTypeName, questionTypeVersion)
	answerType, answerErr := joinSnapshotType(answerTypeName, answerTypeVersion)
	if questionErr != nil || answerErr != nil || questionType != evidence.Question.Type || answerType != evidence.Answer.Type ||
		questionDigest != evidence.Question.Digest.String() || answerDigest != evidence.Answer.Digest.String() {
		return publisher.ErrInvalidRequest
	}
	return nil
}

func getAgentPublication(
	ctx context.Context,
	queryer snapshotQueryer,
	operationKey string,
	forUpdate bool,
) (agentPublicationRecord, bool, error) {
	query := `SELECT ` + agentPublicationColumns + agentPublicationFrom + `
		WHERE p.operation_key = $1
		  AND occurrence.id = p.lease_owner_occurrence_id`
	if forUpdate {
		query += ` FOR UPDATE OF p`
	}
	record, err := scanAgentPublication(queryer.QueryRowContext(ctx, query, operationKey))
	if errors.Is(err, sql.ErrNoRows) {
		return agentPublicationRecord{}, false, nil
	}
	return record, err == nil, err
}

func getAgentPublicationOccurrence(
	ctx context.Context,
	queryer snapshotQueryer,
	occurrenceID int64,
	forUpdate bool,
) (agentPublicationRecord, bool, error) {
	query := `SELECT ` + agentPublicationColumns + agentPublicationFrom + `
		WHERE occurrence.id = $1`
	if forUpdate {
		query += ` FOR UPDATE OF p, occurrence`
	}
	record, err := scanAgentPublication(queryer.QueryRowContext(ctx, query, occurrenceID))
	if errors.Is(err, sql.ErrNoRows) {
		return agentPublicationRecord{}, false, nil
	}
	return record, err == nil, err
}

func scanAgentPublication(row scannable) (agentPublicationRecord, error) {
	var record agentPublicationRecord
	publication := &record.publication
	var operationKind sql.NullString
	var operationPayload []byte
	var publisherType string
	var snapshotID int64
	var snapshotTypeName string
	var snapshotTypeVersion int
	var digest string
	var destination string
	var mode string
	var parameters []byte
	var policyVersion string
	var approvedBy sql.NullString
	var approvalWaitID sql.NullInt64
	var approvalQuestionID, approvalAnswerID sql.NullInt64
	var approvalQuestionTypeName, approvalAnswerTypeName, approvalQuestionDigest, approvalAnswerDigest sql.NullString
	var approvalQuestionTypeVersion, approvalAnswerTypeVersion sql.NullInt64
	var approvalResolvedAt sql.NullTime
	var authority publisher.Authority
	var status string
	var leaseUntil sql.NullTime
	var result []byte
	err := row.Scan(
		&record.operationID, &publication.ID, &publication.OperationKey,
		&operationKind, &operationPayload, &publisherType,
		&snapshotID, &snapshotTypeName, &snapshotTypeVersion, &digest,
		&destination, &mode, &parameters, &policyVersion, &approvedBy,
		&approvalWaitID, &approvalQuestionID, &approvalQuestionTypeName, &approvalQuestionTypeVersion,
		&approvalQuestionDigest, &approvalAnswerID, &approvalAnswerTypeName, &approvalAnswerTypeVersion,
		&approvalAnswerDigest, &approvalResolvedAt,
		&authority.TeamID, &authority.TeamName, &authority.WorkflowRunID,
		&authority.BuildID, &authority.Actor,
		&status, &publication.Attempt, &leaseUntil, &result,
		&publication.CreatedAt, &publication.UpdatedAt,
	)
	if err != nil {
		return agentPublicationRecord{}, err
	}
	typ, err := joinSnapshotType(snapshotTypeName, snapshotTypeVersion)
	if err != nil {
		return agentPublicationRecord{}, err
	}
	primary := snapshot.SnapshotRef{
		ID: snapshot.SnapshotID(snapshotID), Type: typ, Digest: snapshot.Digest(digest),
	}
	publication.Status = publisher.Status(status)
	if leaseUntil.Valid {
		publication.LeaseUntil = leaseUntil.Time.UTC()
	}
	if publication.Status != publisher.StatusPending {
		if err := json.Unmarshal(result, &publication.Result); err != nil {
			return agentPublicationRecord{}, fmt.Errorf("db: decode publication result: %w", err)
		}
		if publication.Result.Status != publication.Status || publication.Result.Validate() != nil {
			return agentPublicationRecord{}, fmt.Errorf("db: publication result is invalid")
		}
	}
	if record.operationID <= 0 || publication.ID <= 0 || publication.Attempt <= 0 ||
		publication.CreatedAt.IsZero() || publication.UpdatedAt.IsZero() ||
		(publication.Status == publisher.StatusPending) != leaseUntil.Valid {
		return agentPublicationRecord{}, fmt.Errorf("db: publication row is invalid")
	}

	approvalPresent := approvedBy.Valid || approvalWaitID.Valid || approvalQuestionID.Valid ||
		approvalAnswerID.Valid || approvalResolvedAt.Valid ||
		approvalQuestionTypeName.Valid || approvalQuestionTypeVersion.Valid || approvalQuestionDigest.Valid ||
		approvalAnswerTypeName.Valid || approvalAnswerTypeVersion.Valid || approvalAnswerDigest.Valid
	// The provider-native pull-request union that once shared this table is
	// gone. A row that still carries its discriminator is not a publication
	// this store can rehydrate, so report it as absent — exactly what the
	// removed union did once Get and Complete saw a PR action. The columns
	// themselves are dropped by the schema removal that follows this change.
	if operationKind.Valid || len(operationPayload) > 0 {
		return agentPublicationRecord{}, sql.ErrNoRows
	}
	publication.Request = publisher.Request{
		Publisher:             snapshot.TypeRef(publisherType),
		Input:                 primary,
		Destination:           destination,
		Mode:                  publisher.Mode(mode),
		ApprovalPolicyVersion: policyVersion,
		ApprovedBy:            approvedBy.String,
		Authority:             authority,
	}
	if approvalPresent {
		if !approvalWaitID.Valid || !approvalQuestionID.Valid || !approvalQuestionTypeName.Valid ||
			!approvalQuestionTypeVersion.Valid || !approvalQuestionDigest.Valid || !approvalAnswerID.Valid ||
			!approvalAnswerTypeName.Valid || !approvalAnswerTypeVersion.Valid || !approvalAnswerDigest.Valid ||
			!approvalResolvedAt.Valid || !approvedBy.Valid {
			return agentPublicationRecord{}, fmt.Errorf("db: publication approval evidence is incomplete")
		}
		questionType, err := joinSnapshotType(
			approvalQuestionTypeName.String, int(approvalQuestionTypeVersion.Int64),
		)
		if err != nil {
			return agentPublicationRecord{}, err
		}
		answerType, err := joinSnapshotType(
			approvalAnswerTypeName.String, int(approvalAnswerTypeVersion.Int64),
		)
		if err != nil {
			return agentPublicationRecord{}, err
		}
		publication.Request.Approval = &publisher.ApprovalEvidence{
			WaitID: workflowwait.ID(approvalWaitID.Int64),
			Question: snapshot.SnapshotRef{
				ID:   snapshot.SnapshotID(approvalQuestionID.Int64),
				Type: questionType, Digest: snapshot.Digest(approvalQuestionDigest.String),
			},
			Answer: snapshot.SnapshotRef{
				ID:   snapshot.SnapshotID(approvalAnswerID.Int64),
				Type: answerType, Digest: snapshot.Digest(approvalAnswerDigest.String),
			},
			ResolvedBy: approvedBy.String, ResolvedAt: approvalResolvedAt.Time.UTC(),
		}
	}
	if err := json.Unmarshal(parameters, &publication.Request.Parameters); err != nil {
		return agentPublicationRecord{}, fmt.Errorf("db: decode publication parameters: %w", err)
	}
	if publication.Request.ValidatePersisted() != nil {
		return agentPublicationRecord{}, fmt.Errorf("db: publication request is invalid")
	}
	key, err := publication.Request.OperationKey()
	if err != nil || key != publication.OperationKey {
		return agentPublicationRecord{}, fmt.Errorf("db: publication operation identity is invalid")
	}
	record.publication = publication.Clone()
	return record, nil
}

func publicationStatusTerminal(status publisher.Status) bool {
	switch status {
	case publisher.StatusSucceeded, publisher.StatusFailed, publisher.StatusStaleBase, publisher.StatusRebaseRequired:
		return true
	default:
		return false
	}
}

var _ publisher.Store = (*agentPublicationsFactory)(nil)
