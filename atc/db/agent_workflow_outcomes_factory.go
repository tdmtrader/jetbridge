package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/workflowoutcomes"
	"github.com/concourse/concourse/agent/snapshot"
)

type AgentWorkflowOutcomesFactory interface {
	workflowoutcomes.Store
	workflowoutcomes.Authorizer
}

func NewAgentWorkflowOutcomesFactory(conn DbConn) AgentWorkflowOutcomesFactory {
	return &agentWorkflowOutcomesFactory{conn: conn}
}

type agentWorkflowOutcomesFactory struct {
	conn DbConn
}

func (factory *agentWorkflowOutcomesFactory) AuthorizeRun(
	ctx context.Context,
	teamID int,
	workflowName string,
	runID snapshot.WorkflowRunID,
) (bool, error) {
	if ctx == nil || teamID <= 0 || workflowName == "" || runID.Validate() != nil {
		return false, nil
	}
	var authorized bool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM agent_workflow_runs
			WHERE id = $1 AND team_id = $2 AND workflow_name = $3 AND definition_kind = 'workflow'
		)
	`, int64(runID), teamID, workflowName).Scan(&authorized)
	return authorized, err
}

func (factory *agentWorkflowOutcomesFactory) AuthorizeOutput(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
) (bool, error) {
	if ctx == nil || teamID <= 0 || runID.Validate() != nil || outputID.Validate() != nil {
		return false, nil
	}
	var authorized bool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs run
			JOIN agent_workflow_run_snapshots binding
			  ON binding.workflow_run_id = run.id
			 AND binding.direction = 'output'
			 AND binding.snapshot_id = $3
			 AND binding.promoted_at IS NOT NULL
			WHERE run.id = $2 AND run.team_id = $1 AND run.definition_kind = 'workflow'
		)
	`, teamID, int64(runID), int64(outputID)).Scan(&authorized)
	return authorized, err
}

type outcomeQueryRower interface {
	QueryRowContext(context.Context, string, ...any) sq.RowScanner
}

const workflowOutcomeColumns = `workflow_run_id, output_snapshot_id, disposition,
	publication_state, publication_id, human_modified, modification_snapshot_id,
	intervention_count, labels, actor, revision, audited_at`

const prefixedWorkflowOutcomeColumns = `outcome.workflow_run_id, outcome.output_snapshot_id, outcome.disposition,
	outcome.publication_state, outcome.publication_id, outcome.human_modified, outcome.modification_snapshot_id,
	outcome.intervention_count, outcome.labels, outcome.actor, outcome.revision, outcome.audited_at`

const (
	maximumWorkflowOutcomeLineageDepth = 64
	maximumWorkflowOutcomeLineageNodes = 1024
)

func (factory *agentWorkflowOutcomesFactory) Record(
	ctx context.Context,
	teamID int,
	request workflowoutcomes.RecordRequest,
) (workflowoutcomes.Outcome, bool, error) {
	if ctx == nil {
		return workflowoutcomes.Outcome{}, false, fmt.Errorf("%w: context is required", workflowoutcomes.ErrInvalidOutcome)
	}
	if err := ctx.Err(); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	request.Labels = append([]string(nil), request.Labels...)
	sort.Strings(request.Labels)
	if request.Labels == nil {
		request.Labels = []string{}
	}
	if err := validateWorkflowOutcomeRequest(teamID, request); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	labels, err := json.Marshal(request.Labels)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	defer Rollback(tx)
	if err := lockWorkflowOutcome(ctx, tx, teamID, request.WorkflowRunID, request.OutputSnapshotID); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	authorized, err := authorizeWorkflowOutcomeRequest(ctx, tx, teamID, request)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	if !authorized {
		return workflowoutcomes.Outcome{}, false, workflowoutcomes.ErrOutcomeNotFound
	}
	existing, found, err := getWorkflowOutcome(ctx, tx, teamID, request.WorkflowRunID, request.OutputSnapshotID, true)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	if found && workflowOutcomeMatches(existing, request) {
		if err := tx.Commit(); err != nil {
			return workflowoutcomes.Outcome{}, false, err
		}
		return existing, false, nil
	}

	var row scannable
	created := !found
	if created {
		row = tx.QueryRowContext(ctx, `
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, publication_id, human_modified, modification_snapshot_id,
				 intervention_count, labels, actor, revision, audited_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, 1, now())
			RETURNING `+workflowOutcomeColumns,
			teamID, int64(request.WorkflowRunID), int64(request.OutputSnapshotID), request.Disposition,
			request.PublicationState, nullableOutcomeInt64(request.PublicationID), request.HumanModified,
			nullableOutcomeSnapshotID(request.ModificationSnapshotID), request.InterventionCount, labels, request.Actor,
		)
	} else {
		row = tx.QueryRowContext(ctx, `
			UPDATE agent_workflow_outcomes
			SET disposition = $4,
				human_modified = $5, modification_snapshot_id = $6,
				intervention_count = $7, labels = $8::jsonb, actor = $9,
				revision = revision + 1, audited_at = now()
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
			RETURNING `+workflowOutcomeColumns,
			teamID, int64(request.WorkflowRunID), int64(request.OutputSnapshotID), request.Disposition,
			request.HumanModified, nullableOutcomeSnapshotID(request.ModificationSnapshotID),
			request.InterventionCount, labels, request.Actor,
		)
	}
	outcome, err := scanWorkflowOutcome(row)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	return outcome, created, nil
}

func (factory *agentWorkflowOutcomesFactory) Modify(
	ctx context.Context,
	teamID int,
	request workflowoutcomes.ModifyRequest,
) (workflowoutcomes.Outcome, bool, error) {
	if ctx == nil {
		return workflowoutcomes.Outcome{}, false, fmt.Errorf("%w: context is required", workflowoutcomes.ErrInvalidOutcome)
	}
	if err := ctx.Err(); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	request.Labels = append([]string(nil), request.Labels...)
	sort.Strings(request.Labels)
	if request.Labels == nil {
		request.Labels = []string{}
	}
	if err := validateWorkflowOutcomeModificationRequest(teamID, request); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	labels, err := json.Marshal(request.Labels)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	defer Rollback(tx)
	if err := lockWorkflowOutcome(ctx, tx, teamID, request.WorkflowRunID, request.OutputSnapshotID); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	authorizationRequest := workflowoutcomes.RecordRequest{
		WorkflowRunID: request.WorkflowRunID, OutputSnapshotID: request.OutputSnapshotID,
		Disposition: request.Disposition, PublicationState: workflowoutcomes.PublicationNotRequested,
		HumanModified: request.HumanModified, ModificationSnapshotID: request.ModificationSnapshotID,
		InterventionCount: 1, Labels: request.Labels, Actor: request.Actor,
	}
	authorized, err := authorizeWorkflowOutcomeRequest(ctx, tx, teamID, authorizationRequest)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	if !authorized {
		return workflowoutcomes.Outcome{}, false, workflowoutcomes.ErrOutcomeNotFound
	}
	existing, found, err := getWorkflowOutcome(ctx, tx, teamID, request.WorkflowRunID, request.OutputSnapshotID, true)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	if found && workflowOutcomeMatchesModification(existing, request) {
		if err := tx.Commit(); err != nil {
			return workflowoutcomes.Outcome{}, false, err
		}
		return existing, false, nil
	}

	var row scannable
	created := !found
	if created {
		row = tx.QueryRowContext(ctx, `
			INSERT INTO agent_workflow_outcomes
				(team_id, workflow_run_id, output_snapshot_id, disposition,
				 publication_state, human_modified, modification_snapshot_id,
				 intervention_count, labels, actor, revision, audited_at)
			VALUES ($1, $2, $3, $4, 'not_requested', $5, $6, 1, $7::jsonb, $8, 1, now())
			RETURNING `+workflowOutcomeColumns,
			teamID, int64(request.WorkflowRunID), int64(request.OutputSnapshotID), request.Disposition,
			request.HumanModified, nullableOutcomeSnapshotID(request.ModificationSnapshotID), labels, request.Actor,
		)
	} else {
		row = tx.QueryRowContext(ctx, `
			UPDATE agent_workflow_outcomes
			SET disposition = $4, human_modified = $5, modification_snapshot_id = $6,
				intervention_count = intervention_count + 1,
				labels = $7::jsonb, actor = $8,
				revision = revision + 1, audited_at = now()
			WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3
			RETURNING `+workflowOutcomeColumns,
			teamID, int64(request.WorkflowRunID), int64(request.OutputSnapshotID), request.Disposition,
			request.HumanModified, nullableOutcomeSnapshotID(request.ModificationSnapshotID), labels, request.Actor,
		)
	}
	outcome, err := scanWorkflowOutcome(row)
	if err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return workflowoutcomes.Outcome{}, false, err
	}
	return outcome, created, nil
}

func (factory *agentWorkflowOutcomesFactory) Get(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
) (workflowoutcomes.Outcome, bool, error) {
	if ctx == nil {
		return workflowoutcomes.Outcome{}, false, fmt.Errorf("%w: context is required", workflowoutcomes.ErrInvalidOutcome)
	}
	if teamID <= 0 || runID.Validate() != nil || outputID.Validate() != nil {
		return workflowoutcomes.Outcome{}, false, workflowoutcomes.ErrInvalidOutcome
	}
	return getWorkflowOutcome(ctx, factory.conn, teamID, runID, outputID, false)
}

func (factory *agentWorkflowOutcomesFactory) ListByRun(
	ctx context.Context,
	teamID int,
	runID snapshot.WorkflowRunID,
) ([]workflowoutcomes.Outcome, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", workflowoutcomes.ErrInvalidOutcome)
	}
	if teamID <= 0 || runID.Validate() != nil {
		return nil, workflowoutcomes.ErrInvalidOutcome
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT `+prefixedWorkflowOutcomeColumns+`
		FROM agent_workflow_outcomes outcome
		WHERE outcome.team_id = $1 AND outcome.workflow_run_id = $2
		  AND EXISTS (
			SELECT 1
			FROM agent_workflow_run_snapshots binding
			WHERE binding.workflow_run_id = outcome.workflow_run_id
			  AND binding.direction = 'output'
			  AND binding.snapshot_id = outcome.output_snapshot_id
			  AND binding.promoted_at IS NOT NULL
		  )
		ORDER BY outcome.output_snapshot_id
	`, teamID, int64(runID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	outcomes := make([]workflowoutcomes.Outcome, 0)
	for rows.Next() {
		outcome, err := scanWorkflowOutcome(rows)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, rows.Err()
}

func authorizeWorkflowOutcomeRequest(
	ctx context.Context,
	queryable Tx,
	teamID int,
	request workflowoutcomes.RecordRequest,
) (bool, error) {
	var outputAuthorized bool
	err := queryable.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs run
			JOIN agent_workflow_run_snapshots binding
			  ON binding.workflow_run_id = run.id
			 AND binding.direction = 'output'
			 AND binding.snapshot_id = $3
			 AND binding.promoted_at IS NOT NULL
			WHERE run.id = $2 AND run.team_id = $1
		)
	`, teamID, int64(request.WorkflowRunID), int64(request.OutputSnapshotID)).Scan(&outputAuthorized)
	if err != nil || !outputAuthorized || request.ModificationSnapshotID == nil {
		return outputAuthorized, err
	}
	return authorizeWorkflowOutcomeModification(
		ctx,
		queryable,
		teamID,
		request.WorkflowRunID,
		request.OutputSnapshotID,
		*request.ModificationSnapshotID,
	)
}

func authorizeWorkflowOutcomeModification(
	ctx context.Context,
	queryable Tx,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
	modificationID snapshot.SnapshotID,
) (bool, error) {
	var authorized bool
	err := queryable.QueryRowContext(ctx, `
		WITH RECURSIVE modification_ancestry(snapshot_id, depth, path) AS (
			SELECT $4::bigint, 0, ARRAY[$4::bigint]
			UNION ALL
			SELECT lineage.input_snapshot_id, descendant.depth + 1,
			       descendant.path || lineage.input_snapshot_id
			FROM modification_ancestry descendant
			JOIN agent_snapshot_productions production
			  ON production.snapshot_id = descendant.snapshot_id
			 AND production.team_id = $1
			JOIN agent_snapshot_lineage lineage
			  ON lineage.production_id = production.id
			WHERE descendant.depth < $5
			  AND NOT lineage.input_snapshot_id = ANY(descendant.path)
		), bounded_ancestry AS MATERIALIZED (
			SELECT snapshot_id, depth, path
			FROM modification_ancestry
			LIMIT $6
		), ancestry_stats AS (
			SELECT
				count(*) AS node_count,
				COALESCE(bool_or(snapshot_id = $3), false) AS contains_original,
				COALESCE(bool_or(
					depth = $5 AND EXISTS (
						SELECT 1
						FROM agent_snapshot_productions overflow_production
						JOIN agent_snapshot_lineage overflow_lineage
						  ON overflow_lineage.production_id = overflow_production.id
						WHERE overflow_production.snapshot_id = bounded_ancestry.snapshot_id
						  AND overflow_production.team_id = $1
						  AND NOT overflow_lineage.input_snapshot_id = ANY(bounded_ancestry.path)
					)
				), false) AS depth_overflow
			FROM bounded_ancestry
		)
		SELECT EXISTS (
			SELECT 1
			FROM ancestry_stats
			JOIN agent_workflow_runs run ON true
			JOIN agent_workflow_run_snapshots binding
			 ON binding.workflow_run_id = run.id
			 AND binding.direction = 'output'
			 AND binding.snapshot_id = $3
			 AND binding.promoted_at IS NOT NULL
			JOIN agent_snapshots original ON original.id = binding.snapshot_id
			JOIN agent_snapshots modification ON modification.id = $4 AND modification.team_id = run.team_id
			WHERE run.id = $2
			  AND run.team_id = $1
			  AND ancestry_stats.node_count < $6
			  AND NOT ancestry_stats.depth_overflow
			  AND ancestry_stats.contains_original
			  AND modification.id <> original.id
			  AND modification.type_name = original.type_name
			  AND modification.type_version = original.type_version
		)
	`, teamID, int64(runID), int64(outputID), int64(modificationID),
		maximumWorkflowOutcomeLineageDepth, maximumWorkflowOutcomeLineageNodes+1).Scan(&authorized)
	return authorized, err
}

func getWorkflowOutcome(
	ctx context.Context,
	queryable outcomeQueryRower,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
	forUpdate bool,
) (workflowoutcomes.Outcome, bool, error) {
	locking := ""
	if forUpdate {
		locking = " FOR UPDATE OF outcome"
	}
	row := queryable.QueryRowContext(ctx, `
		SELECT `+prefixedWorkflowOutcomeColumns+`
		FROM agent_workflow_outcomes outcome
		WHERE outcome.team_id = $1 AND outcome.workflow_run_id = $2
		  AND outcome.output_snapshot_id = $3
		  AND EXISTS (
			SELECT 1
			FROM agent_workflow_run_snapshots binding
			WHERE binding.workflow_run_id = outcome.workflow_run_id
			  AND binding.direction = 'output'
			  AND binding.snapshot_id = outcome.output_snapshot_id
			  AND binding.promoted_at IS NOT NULL
		  )`+locking,
		teamID, int64(runID), int64(outputID),
	)
	outcome, err := scanWorkflowOutcome(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowoutcomes.Outcome{}, false, nil
	}
	return outcome, err == nil, err
}

func getWorkflowOutcomeRaw(
	ctx context.Context,
	queryable outcomeQueryRower,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
	forUpdate bool,
) (workflowoutcomes.Outcome, bool, error) {
	locking := ""
	if forUpdate {
		locking = " FOR UPDATE"
	}
	row := queryable.QueryRowContext(ctx, `
		SELECT `+workflowOutcomeColumns+`
		FROM agent_workflow_outcomes
		WHERE team_id = $1 AND workflow_run_id = $2 AND output_snapshot_id = $3`+locking,
		teamID, int64(runID), int64(outputID),
	)
	outcome, err := scanWorkflowOutcome(row)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowoutcomes.Outcome{}, false, nil
	}
	return outcome, err == nil, err
}

func scanWorkflowOutcome(row scannable) (workflowoutcomes.Outcome, error) {
	var (
		runID, outputID  int64
		disposition      string
		publicationState string
		publicationID    sql.NullInt64
		modificationID   sql.NullInt64
		labels           []byte
		outcome          workflowoutcomes.Outcome
	)
	if err := row.Scan(
		&runID, &outputID, &disposition, &publicationState, &publicationID,
		&outcome.HumanModified, &modificationID, &outcome.InterventionCount,
		&labels, &outcome.Actor, &outcome.Revision, &outcome.AuditedAt,
	); err != nil {
		return workflowoutcomes.Outcome{}, err
	}
	outcome.WorkflowRunID = snapshot.WorkflowRunID(runID)
	outcome.OutputSnapshotID = snapshot.SnapshotID(outputID)
	outcome.Disposition = workflowoutcomes.Disposition(disposition)
	outcome.PublicationState = workflowoutcomes.PublicationState(publicationState)
	if publicationID.Valid {
		value := snapshot.DatabaseID(publicationID.Int64)
		outcome.PublicationID = &value
	}
	if modificationID.Valid {
		value := snapshot.SnapshotID(modificationID.Int64)
		outcome.ModificationSnapshotID = &value
	}
	if err := json.Unmarshal(labels, &outcome.Labels); err != nil {
		return workflowoutcomes.Outcome{}, fmt.Errorf("db: decode workflow outcome labels: %w", err)
	}
	if outcome.Labels == nil {
		outcome.Labels = []string{}
	}
	if err := outcome.Validate(); err != nil {
		return workflowoutcomes.Outcome{}, fmt.Errorf("db: invalid workflow outcome: %w", err)
	}
	return outcome, nil
}

func validateWorkflowOutcomeRequest(teamID int, request workflowoutcomes.RecordRequest) error {
	if teamID <= 0 {
		return fmt.Errorf("%w: team ID must be positive", workflowoutcomes.ErrInvalidOutcome)
	}
	if request.PublicationState != workflowoutcomes.PublicationNotRequested || request.PublicationID != nil {
		return fmt.Errorf("%w: publication evidence is publisher-owned", workflowoutcomes.ErrInvalidOutcome)
	}
	candidate := workflowoutcomes.Outcome{
		WorkflowRunID: request.WorkflowRunID, OutputSnapshotID: request.OutputSnapshotID,
		Disposition: request.Disposition, PublicationState: request.PublicationState,
		PublicationID: request.PublicationID, HumanModified: request.HumanModified,
		ModificationSnapshotID: request.ModificationSnapshotID,
		InterventionCount:      request.InterventionCount, Labels: request.Labels,
		Actor: request.Actor, Revision: 1, AuditedAt: time.Unix(1, 0),
	}
	return candidate.Validate()
}

func validateWorkflowOutcomeModificationRequest(teamID int, request workflowoutcomes.ModifyRequest) error {
	if teamID <= 0 {
		return fmt.Errorf("%w: team ID must be positive", workflowoutcomes.ErrInvalidOutcome)
	}
	candidate := workflowoutcomes.Outcome{
		WorkflowRunID: request.WorkflowRunID, OutputSnapshotID: request.OutputSnapshotID,
		Disposition: request.Disposition, PublicationState: workflowoutcomes.PublicationNotRequested,
		HumanModified: request.HumanModified, ModificationSnapshotID: request.ModificationSnapshotID,
		InterventionCount: 1, Labels: request.Labels, Actor: request.Actor,
		Revision: 1, AuditedAt: time.Unix(1, 0),
	}
	return candidate.Validate()
}

func workflowOutcomeMatches(outcome workflowoutcomes.Outcome, request workflowoutcomes.RecordRequest) bool {
	if outcome.Disposition != request.Disposition ||
		outcome.HumanModified != request.HumanModified || outcome.InterventionCount != request.InterventionCount ||
		outcome.Actor != request.Actor ||
		!equalOutcomeSnapshotID(outcome.ModificationSnapshotID, request.ModificationSnapshotID) ||
		len(outcome.Labels) != len(request.Labels) {
		return false
	}
	for index := range outcome.Labels {
		if outcome.Labels[index] != request.Labels[index] {
			return false
		}
	}
	return true
}

func workflowOutcomeMatchesModification(outcome workflowoutcomes.Outcome, request workflowoutcomes.ModifyRequest) bool {
	if outcome.Disposition != request.Disposition || outcome.HumanModified != request.HumanModified ||
		outcome.Actor != request.Actor ||
		!equalOutcomeSnapshotID(outcome.ModificationSnapshotID, request.ModificationSnapshotID) ||
		len(outcome.Labels) != len(request.Labels) {
		return false
	}
	for index := range outcome.Labels {
		if outcome.Labels[index] != request.Labels[index] {
			return false
		}
	}
	return true
}

func lockWorkflowOutcome(
	ctx context.Context,
	tx Tx,
	teamID int,
	runID snapshot.WorkflowRunID,
	outputID snapshot.SnapshotID,
) error {
	_, err := tx.ExecContext(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended(
			'workflow-outcome:' || $1::bigint::text || ':' || $2::bigint::text || ':' || $3::bigint::text, 0
		))
	`, teamID, int64(runID), int64(outputID))
	return err
}

func nullableOutcomeInt64(value *snapshot.DatabaseID) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func nullableOutcomeSnapshotID(value *snapshot.SnapshotID) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func equalOutcomeInt64(left, right *snapshot.DatabaseID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalOutcomeSnapshotID(left, right *snapshot.SnapshotID) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
