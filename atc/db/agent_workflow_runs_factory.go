package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc/db/encryption"
)

var ErrAgentWorkflowRunExperimentAdmissionClosed = errors.New("db: experiment workflow-run admission is closed")

//counterfeiter:generate . AgentWorkflowRunsFactory
type AgentWorkflowRunsFactory interface {
	CreateWithInputs(context.Context, AgentWorkflowRunCreateRequest) (AgentWorkflowRun, bool, error)
	FindByIdempotencyKey(context.Context, int, string) (AgentWorkflowRun, bool, error)
	FindByIdempotencyKeyKind(context.Context, int, workflow.DefinitionKind, string) (AgentWorkflowRun, bool, error)
	Get(context.Context, int, snapshot.WorkflowRunID) (AgentWorkflowRun, bool, error)
	GetKind(context.Context, int, workflow.DefinitionKind, snapshot.WorkflowRunID) (AgentWorkflowRun, bool, error)
	List(context.Context, AgentWorkflowRunListFilter) ([]AgentWorkflowRun, error)
	ListKind(context.Context, workflow.DefinitionKind, AgentWorkflowRunListFilter) ([]AgentWorkflowRun, error)
	CountByStatus(context.Context, AgentWorkflowRunCountFilter) (map[AgentWorkflowRunStatus]int64, error)
	LinkExecution(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunExecutionLink) error
	RecordPlan(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunPlan) error
	CaptureExecutionStatus(context.Context, int64, BuildStatus) (AgentWorkflowRunBuildCaptureResult, error)
	Transition(context.Context, snapshot.WorkflowRunID, AgentWorkflowRunStatus, AgentWorkflowRunStatus, string) (bool, error)
	ClaimForReconciliation(context.Context, time.Time, time.Duration, int) ([]snapshot.WorkflowRunID, error)
	InspectForReconciliation(context.Context, snapshot.WorkflowRunID) (AgentWorkflowRunReconciliationView, bool, error)
	AdvanceAdmission(context.Context, snapshot.WorkflowRunID) (bool, error)
	Finalize(context.Context, AgentWorkflowRunFinalization) (AgentWorkflowRunFinalizationResult, bool, error)
	Snapshots(context.Context, snapshot.WorkflowRunID) ([]AgentWorkflowRunSnapshotBinding, error)
	InputBindingMatches(context.Context, int, int, snapshot.WorkflowRunID, string, *snapshot.SnapshotRef) (bool, error)
	ValidateCancellationTarget(context.Context, int, snapshot.WorkflowRunID, int64) (bool, error)
}

func NewAgentWorkflowRunsFactory(conn DbConn) AgentWorkflowRunsFactory {
	return &agentWorkflowRunsFactory{conn: conn}
}

type agentWorkflowRunsFactory struct {
	conn DbConn
}

func (factory *agentWorkflowRunsFactory) CreateWithInputs(
	ctx context.Context,
	request AgentWorkflowRunCreateRequest,
) (AgentWorkflowRun, bool, error) {
	if request.DefinitionKind == "" {
		request.DefinitionKind = workflow.DefinitionKindWorkflow
	}
	if err := request.Validate(); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	defer Rollback(tx)

	if err := lockWorkflowRunIdempotency(ctx, tx, request.TeamID, request.DefinitionKind, request.IdempotencyKey); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateWorkflowRunDefinitionKind(ctx, tx, request.WorkflowDefinitionID, request.DefinitionKind); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := lockOpenExperimentWorkflowRunAdmission(ctx, tx, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	existing, found, err := findWorkflowRunByIdempotencyKey(
		ctx, tx, tx.EncryptionStrategy(), request.TeamID, request.DefinitionKind, request.IdempotencyKey, true,
	)
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if found {
		if err := validateIdempotentWorkflowRun(existing, request); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		if err := validateWorkflowRunBindings(ctx, tx, existing.ID, request.Inputs); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		return existing, false, nil
	}

	if err := lockWorkflowRunInputDigests(ctx, tx, request.Inputs); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateWorkflowRunTarget(ctx, tx, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateWorkflowRunResourceSourceAdmission(ctx, tx, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateWorkflowRunInputs(ctx, tx, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_runs
			(definition_kind, team_id, team_name, workflow_definition_id, workflow_name,
			 workflow_version, schema_version, signature_version,
			 definition_content_hash, function_id, idempotency_key,
			 parameterized_config, parameterized_config_hash,
			 dev_validation_provenance_hash,
			 resource_source_admission_id,
			 origin_kind, origin_reference, created_by, status,
			 retry_of_workflow_run_id, ticket_id, ticket_reference)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		ON CONFLICT (team_id, definition_kind, idempotency_key) DO NOTHING
	`, string(request.DefinitionKind), request.TeamID, request.TeamName, request.WorkflowDefinitionID, request.WorkflowName,
		request.WorkflowVersion, request.SchemaVersion, request.SignatureVersion,
		request.DefinitionContentHash, optionalString(request.FunctionID), request.IdempotencyKey,
		[]byte(request.ParameterizedConfig), request.ParameterizedConfigHash,
		request.DevValidationProvenanceHash,
		nullableWorkflowRunResourceSourceAdmissionID(request.ResourceSourceAdmissionID),
		request.OriginKind, request.OriginReference, request.CreatedBy, string(request.Status),
		optionalInt64(request.RetryOfWorkflowRunID),
		nullableWorkflowRunTicketID(request.TicketID), request.TicketReference)
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	created := affected == 1
	if !created {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run idempotency serialization did not reserve a unique key")
	}

	run, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE team_id = $1 AND definition_kind = $2 AND idempotency_key = $3
		FOR UPDATE
	`, request.TeamID, string(request.DefinitionKind), request.IdempotencyKey), tx.EncryptionStrategy())
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	if err := validateIdempotentWorkflowRun(run, request); err != nil {
		return AgentWorkflowRun{}, false, err
	}

	ports := sortedSnapshotPorts(request.Inputs)
	for _, port := range ports {
		ref := request.Inputs[port]
		if err := bindWorkflowRunSnapshot(ctx, tx, run.ID, "input", port, ref.ID); err != nil {
			return AgentWorkflowRun{}, false, err
		}
		actor := fmt.Sprintf("workflow-run:%d:input:%s", int64(run.ID), port)
		if err := insertOrVerifyRetention(ctx, tx, request.TeamID, ref.ID, snapshot.RetentionSpec{
			Class: snapshot.RetentionClassRun, WorkflowRunID: &run.ID,
			Actor: actor, Reason: "active workflow-run input",
		}); err != nil {
			return AgentWorkflowRun{}, false, err
		}
	}

	if err := tx.Commit(); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	return run, created, nil
}

func lockOpenExperimentWorkflowRunAdmission(
	ctx context.Context,
	tx Tx,
	request AgentWorkflowRunCreateRequest,
) error {
	if request.ExperimentAdmission == nil {
		return nil
	}
	gate := request.ExperimentAdmission
	var (
		state                              string
		cellStatus                         string
		candidateRun                       sql.NullInt64
		evaluatorRun                       sql.NullInt64
		variantDefinition                  int
		variantName                        string
		variantVersion                     int
		variantFunction                    sql.NullString
		variantConfigHash                  sql.NullString
		variantDevValidationProvenanceHash sql.NullString
		evalDefinition                     int
		evalName                           string
		evalVersion                        int
		evalFunction                       sql.NullString
		evalConfigHash                     sql.NullString
		evalDevValidationProvenanceHash    sql.NullString
		requiresReservation                bool
		hasReservation                     bool
	)
	err := tx.QueryRowContext(ctx, `
		SELECT experiment.state, cell.status, cell.candidate_workflow_run_id,
		       evaluation.evaluator_workflow_run_id,
		       variant.definition_id, variant.workflow_name, variant.workflow_version,
		       variant.function_id, variant.target_config_hash, variant.dev_validation_provenance_hash,
		       experiment.evaluator_definition_id, experiment.evaluator_workflow_name,
		       experiment.evaluator_workflow_version, experiment.evaluator_function_id,
		       experiment.evaluator_target_config_hash, experiment.evaluator_dev_validation_provenance_hash,
		       (experiment.per_cell_budget_usd > 0 OR experiment.total_budget_usd > 0
		        OR experiment.max_tokens_per_cell > 0),
		       EXISTS (
		           SELECT 1 FROM agent_experiment_budget_reservations reservation
		           WHERE reservation.cell_id = cell.id AND reservation.state = 'active'
		       )
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		JOIN agent_experiment_variants variant ON variant.id = cell.variant_id
		LEFT JOIN agent_experiment_evaluations evaluation ON evaluation.cell_id = cell.id
		WHERE experiment.id = $1 AND cell.id = $2 AND experiment.team_id = $3
		FOR UPDATE OF experiment, cell
	`, gate.ExperimentID, gate.CellID, request.TeamID).Scan(
		&state, &cellStatus, &candidateRun, &evaluatorRun,
		&variantDefinition, &variantName, &variantVersion, &variantFunction, &variantConfigHash, &variantDevValidationProvenanceHash,
		&evalDefinition, &evalName, &evalVersion, &evalFunction, &evalConfigHash, &evalDevValidationProvenanceHash,
		&requiresReservation, &hasReservation,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAgentWorkflowRunExperimentAdmissionClosed
	}
	if err != nil {
		return err
	}
	if state != "running" {
		return ErrAgentWorkflowRunExperimentAdmissionClosed
	}
	if requiresReservation && !hasReservation {
		return ErrAgentWorkflowRunExperimentAdmissionClosed
	}
	switch gate.Phase {
	case "candidate":
		if cellStatus != "pending" && cellStatus != "running" || candidateRun.Valid ||
			!experimentAdmissionTargetMatches(
				request, variantDefinition, variantName, variantVersion,
				variantFunction, variantConfigHash, variantDevValidationProvenanceHash,
			) {
			return ErrAgentWorkflowRunExperimentAdmissionClosed
		}
	case "evaluator":
		if cellStatus != "running" || !candidateRun.Valid || evaluatorRun.Valid ||
			!experimentAdmissionTargetMatches(
				request, evalDefinition, evalName, evalVersion,
				evalFunction, evalConfigHash, evalDevValidationProvenanceHash,
			) {
			return ErrAgentWorkflowRunExperimentAdmissionClosed
		}
	default:
		// Request validation rejects this before opening the transaction, but
		// keep the persistence seam independently fail closed.
		return ErrAgentWorkflowRunExperimentAdmissionClosed
	}
	return nil
}

func experimentAdmissionTargetMatches(
	request AgentWorkflowRunCreateRequest,
	definitionID int,
	workflowName string,
	workflowVersion int,
	functionID sql.NullString,
	targetConfigHash sql.NullString,
	devValidationProvenanceHash sql.NullString,
) bool {
	if request.WorkflowDefinitionID != definitionID ||
		request.WorkflowName != workflowName ||
		request.WorkflowVersion != workflowVersion ||
		!targetConfigHash.Valid ||
		request.ParameterizedConfigHash != targetConfigHash.String ||
		!devValidationProvenanceHash.Valid ||
		request.DevValidationProvenanceHash != devValidationProvenanceHash.String {
		return false
	}
	if request.FunctionID == nil {
		return !functionID.Valid
	}
	return functionID.Valid && *request.FunctionID == functionID.String
}

const workflowRunIdempotencyLockDomain = "agent-workflow-run-idempotency/v1\x00"

func lockWorkflowRunIdempotency(ctx context.Context, tx Tx, teamID int, kind workflow.DefinitionKind, key string) error {
	lockKey := snapshotAdvisoryLockKey(
		workflowRunIdempotencyLockDomain,
		fmt.Sprintf("%d\x00%s\x00%s", teamID, kind, key),
	)
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey)
	return err
}

func validateWorkflowRunDefinitionKind(ctx context.Context, tx Tx, definitionID int, kind workflow.DefinitionKind) error {
	var found bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_definitions
			WHERE id = $1 AND definition_kind = $2
		)
	`, definitionID, string(kind)).Scan(&found); err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("db: workflow-run definition %d does not exist", definitionID)
	}
	return nil
}

func lockWorkflowRunInputDigests(
	ctx context.Context,
	tx Tx,
	inputs map[string]snapshot.SnapshotRef,
) error {
	unique := make(map[snapshot.Digest]struct{}, len(inputs))
	for _, ref := range inputs {
		unique[ref.Digest] = struct{}{}
	}
	ordered := make([]snapshot.Digest, 0, len(unique))
	for digest := range unique {
		ordered = append(ordered, digest)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	for _, digest := range ordered {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, snapshotDigestLockKey(digest)); err != nil {
			return fmt.Errorf("db: lock workflow-run input digest %s: %w", digest, err)
		}
	}
	return nil
}

func findWorkflowRunByIdempotencyKey(
	ctx context.Context,
	queryer snapshotQueryer,
	encryptionStrategy encryption.Strategy,
	teamID int,
	kind workflow.DefinitionKind,
	key string,
	lock bool,
) (AgentWorkflowRun, bool, error) {
	query := `SELECT ` + agentWorkflowRunColumns + `
		FROM agent_workflow_runs
		WHERE team_id = $1 AND definition_kind = $2 AND idempotency_key = $3`
	if lock {
		query += ` FOR UPDATE`
	}
	run, err := scanAgentWorkflowRun(queryer.QueryRowContext(ctx, query, teamID, string(kind), key), encryptionStrategy)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRun{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRun{}, false, err
	}
	return run, true, nil
}

func validateWorkflowRunTarget(ctx context.Context, tx Tx, request AgentWorkflowRunCreateRequest) error {
	var teamName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM teams WHERE id = $1`, request.TeamID).Scan(&teamName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run team %d does not exist", request.TeamID)
		}
		return err
	}
	if teamName != request.TeamName {
		return fmt.Errorf("db: workflow-run team name does not match current team")
	}

	var name, hash string
	var version, schemaVersion, signatureVersion int
	err := tx.QueryRowContext(ctx, `
		SELECT name, version, schema_version, signature_version, content_hash
		FROM agent_workflow_definitions
		WHERE id = $1 AND definition_kind = $2
	`, request.WorkflowDefinitionID, string(request.DefinitionKind)).Scan(&name, &version, &schemaVersion, &signatureVersion, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run definition %d does not exist", request.WorkflowDefinitionID)
	}
	if err != nil {
		return err
	}
	if name != request.WorkflowName || version != request.WorkflowVersion ||
		schemaVersion != request.SchemaVersion || signatureVersion != request.SignatureVersion ||
		hash != request.DefinitionContentHash {
		return fmt.Errorf("db: workflow-run copied definition identity does not match the durable definition")
	}

	if request.RetryOfWorkflowRunID != nil {
		var (
			teamID                      int
			workflowDefinitionID        int
			workflowName                string
			workflowVersion             int
			schemaVersion               int
			signatureVersion            int
			definitionContentHash       string
			functionID                  sql.NullString
			devValidationProvenanceHash string
			resourceSourceAdmissionID   sql.NullInt64
			status                      AgentWorkflowRunStatus
		)
		err := tx.QueryRowContext(ctx, `
			SELECT team_id, workflow_definition_id, workflow_name, workflow_version,
			       schema_version, signature_version, definition_content_hash,
			       function_id, dev_validation_provenance_hash,
			       resource_source_admission_id, status
			FROM agent_workflow_runs
			WHERE id = $1 AND definition_kind = $2
			FOR KEY SHARE
		`, int64(*request.RetryOfWorkflowRunID), string(request.DefinitionKind)).Scan(
			&teamID, &workflowDefinitionID, &workflowName, &workflowVersion,
			&schemaVersion, &signatureVersion, &definitionContentHash,
			&functionID, &devValidationProvenanceHash,
			&resourceSourceAdmissionID, &status,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run retry target is absent or belongs to another team")
		}
		if err != nil {
			return err
		}
		if teamID != request.TeamID {
			return fmt.Errorf("db: workflow-run retry target is absent or belongs to another team")
		}
		var sourceFunctionID *string
		if functionID.Valid {
			sourceFunctionID = &functionID.String
		}
		if workflowDefinitionID != request.WorkflowDefinitionID ||
			workflowName != request.WorkflowName ||
			workflowVersion != request.WorkflowVersion ||
			schemaVersion != request.SchemaVersion ||
			signatureVersion != request.SignatureVersion ||
			definitionContentHash != request.DefinitionContentHash ||
			!equalOptionalString(sourceFunctionID, request.FunctionID) ||
			devValidationProvenanceHash != request.DevValidationProvenanceHash {
			return fmt.Errorf("db: workflow-run retry target is incompatible with the requested workflow target")
		}
		var retrySourceAdmissionID *int64
		if resourceSourceAdmissionID.Valid {
			value := resourceSourceAdmissionID.Int64
			retrySourceAdmissionID = &value
		}
		if !equalOptionalWorkflowRunSourceAdmissionID(retrySourceAdmissionID, request.ResourceSourceAdmissionID) {
			return fmt.Errorf("db: workflow-run retry target does not reuse its resource source admission")
		}
		if !isTerminalWorkflowRunStatus(status) {
			return fmt.Errorf("db: workflow-run retry target is not terminal")
		}
		if request.OriginKind != "retry" || request.OriginReference != request.RetryOfWorkflowRunID.String() {
			return fmt.Errorf("db: workflow-run retry origin does not identify its retry target")
		}
		if err := validateWorkflowRunBindings(ctx, tx, *request.RetryOfWorkflowRunID, request.Inputs); err != nil {
			return fmt.Errorf("db: workflow-run retry input bindings do not match the retry target: %w", err)
		}
	}
	return nil
}

func validateWorkflowRunResourceSourceAdmission(
	ctx context.Context,
	tx Tx,
	request AgentWorkflowRunCreateRequest,
) error {
	if request.DefinitionKind == workflow.DefinitionKindNode {
		if request.ResourceSourceAdmissionID != nil {
			return fmt.Errorf(
				"%w: reusable node runs cannot use a resource source admission",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
		var durableKind workflow.DefinitionKind
		err := tx.QueryRowContext(ctx, `
			SELECT definition_kind
			FROM agent_workflow_definitions
			WHERE id = $1
		`, request.WorkflowDefinitionID).Scan(&durableKind)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf(
				"%w: reusable node definition is absent or has the wrong kind",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
		if err != nil {
			return err
		}
		if durableKind != workflow.DefinitionKindNode {
			return fmt.Errorf(
				"%w: reusable node definition is absent or has the wrong kind",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
		// Node imports reject resource and resource-source capabilities, so the
		// durable node kind proves this target is source-free.
		return nil
	}
	if request.ResourceSourceAdmissionID == nil {
		declaresSources, err := workflowRunTargetDeclaresResourceSources(ctx, tx, request.WorkflowDefinitionID)
		if err != nil {
			return err
		}
		if declaresSources {
			return fmt.Errorf(
				"%w: source-bearing workflow runs require a ready resource source admission",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
		return nil
	}
	var status AgentWorkflowResourceSourceAdmissionStatus
	err := tx.QueryRowContext(ctx, `
		SELECT status
		FROM agent_workflow_resource_source_admissions
		WHERE id = $1 AND team_id = $2 AND workflow_definition_id = $3
		FOR SHARE
	`, *request.ResourceSourceAdmissionID, request.TeamID,
		request.WorkflowDefinitionID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf(
			"%w: workflow-run resource source admission is not owned by its team and definition",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}
	if err != nil {
		return err
	}
	if status != AgentWorkflowResourceSourceAdmissionReady {
		return ErrAgentWorkflowResourceSourceNotReady
	}

	rows, err := tx.QueryContext(ctx, `
		SELECT source_name, snapshot_id
		FROM agent_workflow_resource_source_bindings
		WHERE admission_id = $1
		ORDER BY source_name
	`, *request.ResourceSourceAdmissionID)
	if err != nil {
		return err
	}
	defer Close(rows)
	bindings := make(map[string]snapshot.SnapshotID)
	for rows.Next() {
		var (
			port string
			id   sql.NullInt64
		)
		if err := rows.Scan(&port, &id); err != nil {
			return err
		}
		if !id.Valid || id.Int64 <= 0 {
			return fmt.Errorf(
				"%w: ready workflow-run source admission has an incomplete binding",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
		if _, found := bindings[port]; found {
			return fmt.Errorf(
				"%w: ready workflow-run source admission has duplicate bindings",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
		bindings[port] = snapshot.SnapshotID(id.Int64)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(bindings) == 0 {
		return fmt.Errorf(
			"%w: ready workflow-run source admission has no bindings",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}

	target, err := loadWorkflowRunResourceSourceTarget(ctx, tx, request)
	if err != nil {
		return err
	}
	return validateWorkflowRunResourceSourceInputSet(
		request.Inputs,
		target.Signature.Inputs,
		target.Function.ResourceSources,
		bindings,
	)
}

func workflowRunTargetDeclaresResourceSources(
	ctx context.Context,
	queryer snapshotQueryer,
	workflowDefinitionID int,
) (bool, error) {
	var (
		definition   workflow.Definition
		rawYAML      string
		manifestJSON sql.NullString
		compiledJSON sql.NullString
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT id, name, version, content_hash, schema_version,
		       signature_version, definition, source_manifest, compiled_definition
		FROM agent_workflow_definitions
		WHERE id = $1 AND definition_kind = 'workflow'
	`, workflowDefinitionID).Scan(
		&definition.ID,
		&definition.Name,
		&definition.Version,
		&definition.ContentHash,
		&definition.SchemaVersion,
		&definition.SignatureVersion,
		&rawYAML,
		&manifestJSON,
		&compiledJSON,
	)
	if err != nil {
		return false, err
	}

	compiled, _, err := compileStoredWorkflowSource(
		definition.Name,
		definition.Version,
		definition.ContentHash,
		rawYAML,
		manifestJSON,
		compiledJSON,
	)
	if err != nil {
		return false, fmt.Errorf(
			"%w: workflow source definition no longer compiles: %v",
			ErrAgentWorkflowResourceSourceConflict,
			err,
		)
	}
	return len(compiled.Function.ResourceSources) > 0, nil
}

func loadWorkflowRunResourceSourceTarget(
	ctx context.Context,
	queryer snapshotQueryer,
	request AgentWorkflowRunCreateRequest,
) (workflow.FunctionTarget, error) {
	var (
		definition   workflow.Definition
		rawYAML      string
		manifestJSON sql.NullString
		compiledJSON sql.NullString
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT id, name, version, content_hash, schema_version,
		       signature_version, definition, source_manifest, compiled_definition
		FROM agent_workflow_definitions
		WHERE id = $1 AND definition_kind = 'workflow'
	`, request.WorkflowDefinitionID).Scan(
		&definition.ID,
		&definition.Name,
		&definition.Version,
		&definition.ContentHash,
		&definition.SchemaVersion,
		&definition.SignatureVersion,
		&rawYAML,
		&manifestJSON,
		&compiledJSON,
	)
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	compiled, source, err := compileStoredWorkflowSource(
		definition.Name,
		definition.Version,
		definition.ContentHash,
		rawYAML,
		manifestJSON,
		compiledJSON,
	)
	if err != nil {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"%w: ready workflow-run source definition no longer compiles: %v",
			ErrAgentWorkflowResourceSourceConflict,
			err,
		)
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil ||
		metadata.SchemaVersion != definition.SchemaVersion ||
		metadata.SignatureVersion != definition.SignatureVersion {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"%w: ready workflow-run source definition metadata drifted",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}
	populateCompiledWorkflowDefinition(&definition, compiled, source)

	var target workflow.FunctionTarget
	if request.FunctionID == nil {
		target, err = workflow.FullFunctionTarget(definition)
	} else {
		target, err = workflow.ExtractFunctionTarget(definition, *request.FunctionID)
	}
	if err != nil {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"%w: ready workflow-run source target is invalid: %v",
			ErrAgentWorkflowResourceSourceConflict,
			err,
		)
	}
	if target.WorkflowDefinitionID != request.WorkflowDefinitionID ||
		target.WorkflowName != request.WorkflowName ||
		target.WorkflowVersion != request.WorkflowVersion ||
		target.SignatureVersion != request.SignatureVersion {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"%w: ready workflow-run source target identity drifted",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}
	return target, nil
}

func validateWorkflowRunResourceSourceInputSet(
	inputs map[string]snapshot.SnapshotRef,
	publicPorts []workflow.SignaturePort,
	sourcePorts []workflow.ResourceSource,
	bindings map[string]snapshot.SnapshotID,
) error {
	if len(sourcePorts) == 0 || len(bindings) == 0 {
		return fmt.Errorf(
			"%w: ready workflow-run source admission has no bindings",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}

	sources := make(map[string]workflow.ResourceSource, len(sourcePorts))
	for _, source := range sourcePorts {
		sources[source.Name] = source
	}
	if len(bindings) != len(sources) {
		return fmt.Errorf(
			"%w: ready admission bindings differ from workflow source declarations",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}
	for port, id := range bindings {
		if _, declared := sources[port]; !declared || id <= 0 {
			return fmt.Errorf(
				"%w: ready admission bindings differ from workflow source declarations",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
	}

	public := make(map[string]workflow.SignaturePort, len(publicPorts))
	for _, port := range publicPorts {
		public[port.Name] = port
	}
	for port, input := range inputs {
		if source, bound := sources[port]; bound {
			if input.ID != bindings[port] || input.Type != source.Type {
				return fmt.Errorf(
					"%w: workflow-run source inputs differ from ready admission bindings",
					ErrAgentWorkflowResourceSourceConflict,
				)
			}
			continue
		}
		publicPort, declared := public[port]
		if !declared {
			return fmt.Errorf(
				"%w: workflow-run input %q is neither a public input nor a source binding",
				ErrAgentWorkflowResourceSourceConflict,
				port,
			)
		}
		if input.Type != publicPort.Type {
			return fmt.Errorf(
				"%w: workflow-run public input %q differs from its signature",
				ErrAgentWorkflowResourceSourceConflict,
				port,
			)
		}
	}
	for port, binding := range bindings {
		input, found := inputs[port]
		if !found || input.ID != binding {
			return fmt.Errorf(
				"%w: workflow-run source inputs differ from ready admission bindings",
				ErrAgentWorkflowResourceSourceConflict,
			)
		}
	}
	for _, port := range publicPorts {
		if _, found := inputs[port.Name]; !found && !port.Optional {
			return fmt.Errorf(
				"%w: workflow-run required public input %q is missing",
				ErrAgentWorkflowResourceSourceConflict,
				port.Name,
			)
		}
	}
	return nil
}

func isTerminalWorkflowRunStatus(status AgentWorkflowRunStatus) bool {
	switch status {
	case AgentWorkflowRunStatusSucceeded,
		AgentWorkflowRunStatusFailed,
		AgentWorkflowRunStatusErrored,
		AgentWorkflowRunStatusAborted:
		return true
	default:
		return false
	}
}

func validateWorkflowRunInputs(ctx context.Context, tx Tx, request AgentWorkflowRunCreateRequest) error {
	for port, ref := range request.Inputs {
		value, err := authorizedSnapshotByRef(ctx, tx, request.TeamID, ref, true)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: workflow-run input %q is unavailable or unauthorized", port)
		}
		if err != nil {
			return err
		}
		if value.Type != ref.Type || value.Digest != ref.Digest {
			return fmt.Errorf("db: workflow-run input %q does not match its immutable identity", port)
		}
	}
	return nil
}

func validateIdempotentWorkflowRun(run AgentWorkflowRun, request AgentWorkflowRunCreateRequest) error {
	if run.DefinitionKind != request.DefinitionKind || run.TeamID != request.TeamID ||
		run.WorkflowDefinitionID != request.WorkflowDefinitionID ||
		run.WorkflowName != request.WorkflowName || run.WorkflowVersion != request.WorkflowVersion ||
		run.SchemaVersion != request.SchemaVersion || run.SignatureVersion != request.SignatureVersion ||
		run.DefinitionContentHash != request.DefinitionContentHash ||
		!equalOptionalString(run.FunctionID, request.FunctionID) ||
		run.IdempotencyKey != request.IdempotencyKey ||
		!semanticJSONEqual(run.ParameterizedConfig, request.ParameterizedConfig) ||
		run.ParameterizedConfigHash != request.ParameterizedConfigHash ||
		run.DevValidationProvenanceHash != request.DevValidationProvenanceHash ||
		!equalOptionalWorkflowRunSourceAdmissionID(
			run.ResourceSourceAdmissionID,
			request.ResourceSourceAdmissionID,
		) ||
		run.OriginKind != request.OriginKind || run.OriginReference != request.OriginReference ||
		run.CreatedBy != request.CreatedBy ||
		!equalOptionalWorkflowRunTicketID(run.TicketID, request.TicketID) ||
		run.TicketReference != request.TicketReference ||
		!equalWorkflowRunID(run.RetryOfWorkflowRunID, request.RetryOfWorkflowRunID) {
		return fmt.Errorf("db: workflow-run idempotency key conflicts with immutable target, origin, or inputs")
	}
	return nil
}

func validateWorkflowRunBindings(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	expected map[string]snapshot.SnapshotRef,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT b.port_name, b.snapshot_id, s.type_name, s.type_version, s.digest
		FROM agent_workflow_run_snapshots b
		JOIN agent_snapshots s ON s.id = b.snapshot_id
		WHERE b.workflow_run_id = $1 AND b.direction = 'input'
	`, int64(runID))
	if err != nil {
		return err
	}
	defer Close(rows)
	found := make(map[string]snapshot.SnapshotRef)
	for rows.Next() {
		var port, typeName, digest string
		var id int64
		var typeVersion int
		if err := rows.Scan(&port, &id, &typeName, &typeVersion, &digest); err != nil {
			return err
		}
		typeRef, err := joinSnapshotType(typeName, typeVersion)
		if err != nil {
			return err
		}
		found[port] = snapshot.SnapshotRef{
			ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest),
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) != len(expected) {
		return fmt.Errorf("db: workflow-run idempotency key conflicts with immutable input bindings")
	}
	for port, ref := range expected {
		if found[port] != ref {
			return fmt.Errorf("db: workflow-run idempotency key conflicts with immutable input bindings")
		}
	}
	return nil
}

func sortedSnapshotPorts(inputs map[string]snapshot.SnapshotRef) []string {
	ports := make([]string, 0, len(inputs))
	for port := range inputs {
		ports = append(ports, port)
	}
	sort.Strings(ports)
	return ports
}

func optionalString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableWorkflowRunResourceSourceAdmissionID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableWorkflowRunTicketID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func equalOptionalWorkflowRunTicketID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalOptionalWorkflowRunSourceAdmissionID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalWorkflowRunID(left, right *snapshot.WorkflowRunID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (factory *agentWorkflowRunsFactory) FindByIdempotencyKey(
	ctx context.Context,
	teamID int,
	key string,
) (AgentWorkflowRun, bool, error) {
	if teamID <= 0 || strings.TrimSpace(key) == "" {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run team and idempotency key are required")
	}
	return factory.FindByIdempotencyKeyKind(ctx, teamID, workflow.DefinitionKindWorkflow, key)
}

func (factory *agentWorkflowRunsFactory) FindByIdempotencyKeyKind(
	ctx context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	key string,
) (AgentWorkflowRun, bool, error) {
	if teamID <= 0 || strings.TrimSpace(key) == "" ||
		kind != workflow.DefinitionKindWorkflow && kind != workflow.DefinitionKindNode {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run kind, team, and idempotency key are required")
	}
	return findWorkflowRunByIdempotencyKey(
		ctx, factory.conn, factory.conn.EncryptionStrategy(), teamID, kind, key, false,
	)
}

func (factory *agentWorkflowRunsFactory) Get(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
) (AgentWorkflowRun, bool, error) {
	return factory.GetKind(ctx, teamID, workflow.DefinitionKindWorkflow, id)
}

func (factory *agentWorkflowRunsFactory) GetKind(
	ctx context.Context,
	teamID int,
	kind workflow.DefinitionKind,
	id snapshot.WorkflowRunID,
) (AgentWorkflowRun, bool, error) {
	if teamID <= 0 || kind != workflow.DefinitionKindWorkflow && kind != workflow.DefinitionKindNode {
		return AgentWorkflowRun{}, false, fmt.Errorf("db: workflow-run team ID must be positive")
	}
	if err := id.Validate(); err != nil {
		return AgentWorkflowRun{}, false, err
	}
	run, err := scanAgentWorkflowRun(factory.conn.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1 AND team_id = $2 AND definition_kind = $3
	`, int64(id), teamID, string(kind)), factory.conn.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRun{}, false, nil
	}
	return run, err == nil, err
}

// ValidateCancellationTarget verifies the selected build through the full
// durable workflow-run -> pipeline-run -> instance-pipeline -> job chain.
// Cancellation must not rely on a build ID and team alone: both are public
// integer identities and neither proves that the build is the entry job of
// the instance materialized for this workflow run.
func (factory *agentWorkflowRunsFactory) ValidateCancellationTarget(
	ctx context.Context,
	teamID int,
	id snapshot.WorkflowRunID,
	buildID int64,
) (bool, error) {
	if teamID <= 0 || buildID <= 0 {
		return false, fmt.Errorf("db: workflow-run cancellation target requires positive team and build IDs")
	}
	if err := id.Validate(); err != nil {
		return false, err
	}
	var linked bool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs run
			JOIN pipeline_runs execution
			  ON execution.id = run.pipeline_run_id
			 AND execution.template_pipeline_id = run.template_pipeline_id
			 AND execution.instance_pipeline_id = run.instance_pipeline_id
			JOIN pipelines template
			  ON template.id = run.template_pipeline_id
			 AND template.team_id = run.team_id
			JOIN pipelines instance
			  ON instance.id = run.instance_pipeline_id
			 AND instance.team_id = run.team_id
			JOIN builds selected
			  ON selected.id = run.planned_build_id
			 AND selected.pipeline_id = instance.id
			 AND selected.team_id = run.team_id
			JOIN jobs entry
			  ON entry.id = selected.job_id
			 AND entry.pipeline_id = instance.id
			WHERE run.id = $1
			  AND run.team_id = $2
			  AND run.planned_build_id = $3
			  AND run.status = 'canceling'
		)
	`, int64(id), teamID, buildID).Scan(&linked)
	return linked, err
}

func (factory *agentWorkflowRunsFactory) List(
	ctx context.Context,
	filter AgentWorkflowRunListFilter,
) ([]AgentWorkflowRun, error) {
	return factory.ListKind(ctx, workflow.DefinitionKindWorkflow, filter)
}

func (factory *agentWorkflowRunsFactory) ListKind(
	ctx context.Context,
	kind workflow.DefinitionKind,
	filter AgentWorkflowRunListFilter,
) ([]AgentWorkflowRun, error) {
	if kind != workflow.DefinitionKindWorkflow && kind != workflow.DefinitionKindNode {
		return nil, fmt.Errorf("db: workflow-run list requires a definition kind")
	}
	if filter.TeamID <= 0 || filter.Limit < 0 || filter.Limit > 1001 {
		return nil, fmt.Errorf("db: workflow-run list requires a team and a fetch limit from 0 to 1001")
	}
	if filter.Before != nil {
		if err := filter.Before.Validate(); err != nil {
			return nil, err
		}
	}
	if filter.Status != "" {
		if err := filter.Status.Validate(); err != nil {
			return nil, err
		}
	}
	if filter.WorkflowVersion != nil && *filter.WorkflowVersion <= 0 {
		return nil, fmt.Errorf("db: workflow-run list workflow version must be positive")
	}
	if filter.TicketID != nil && *filter.TicketID <= 0 {
		return nil, fmt.Errorf("db: workflow-run list ticket ID must be positive")
	}
	if filter.Scope != "" {
		if err := filter.Scope.Validate(); err != nil {
			return nil, err
		}
	}
	if filter.NodeStatus != "" && filter.NodeID == "" {
		return nil, fmt.Errorf("db: workflow-run list node status filter requires a node")
	}
	if filter.Lens != "" {
		if err := filter.Lens.Validate(); err != nil {
			return nil, err
		}
	}
	query := `SELECT ` + agentWorkflowRunColumns + `
		FROM agent_workflow_runs
		WHERE team_id = $1 AND definition_kind = $2`
	args := []any{filter.TeamID, string(kind)}
	appendFilter := func(column string, value any) {
		args = append(args, value)
		query += ` AND ` + column + ` = $` + strconv.Itoa(len(args))
	}
	placeholders := func(values []string) string {
		rendered := make([]string, 0, len(values))
		for _, value := range values {
			args = append(args, value)
			rendered = append(rendered, `$`+strconv.Itoa(len(args)))
		}
		return strings.Join(rendered, ", ")
	}
	workflowNamePlaceholder := ""
	if filter.WorkflowName != "" {
		appendFilter("workflow_name", filter.WorkflowName)
		workflowNamePlaceholder = `$` + strconv.Itoa(len(args))
	}
	if filter.WorkflowVersion != nil {
		appendFilter("workflow_version", *filter.WorkflowVersion)
	}
	if filter.Status != "" {
		appendFilter("status", string(filter.Status))
	}
	if filter.OriginKind != "" {
		appendFilter("origin_kind", filter.OriginKind)
	}
	if filter.OriginReference != "" {
		appendFilter("origin_reference", filter.OriginReference)
	}
	// One ticket, one query, ordered by run occurrence time — never one query
	// per workflow name.
	if filter.TicketID != nil {
		appendFilter("ticket_id", *filter.TicketID)
	}
	switch filter.Scope {
	case AgentWorkflowRunScopeOperational:
		query += ` AND origin_kind NOT IN (` + placeholders(ExperimentOriginKinds) + `)`
	case AgentWorkflowRunScopeExperiment:
		query += ` AND origin_kind IN (` + placeholders(ExperimentOriginKinds) + `)`
	}
	// History is bounded by completed_at while active runs are unioned in
	// regardless of age. Without the OR, a long-running run created before the
	// window would silently vanish from the page whose primary job is showing
	// what needs attention now.
	if filter.CompletedSince != nil {
		args = append(args, filter.CompletedSince.UTC())
		bound := ` AND completed_at >= $` + strconv.Itoa(len(args))
		if filter.IncludeActiveRuns {
			bound = ` AND (completed_at >= $` + strconv.Itoa(len(args)) + ` OR completed_at IS NULL)`
		}
		query += bound
	}
	// The occurrence projection only ever contains nodes the run's own graph
	// has as execution nodes, so this join is exact by construction and needs
	// no prefix guessing.
	if filter.NodeID != "" {
		args = append(args, filter.NodeID)
		exists := `EXISTS (SELECT 1 FROM agent_workflow_run_node_occurrences o
			WHERE o.workflow_run_id = agent_workflow_runs.id AND o.node_id = $` + strconv.Itoa(len(args))
		if filter.NodeStatus != "" {
			args = append(args, filter.NodeStatus)
			exists += ` AND o.status = $` + strconv.Itoa(len(args))
		}
		query += ` AND ` + exists + `)`
	}
	// The lens is the run-level projection of the node-level attention
	// resolution in agent/workflowrun/occurrence/attention.go. The canvas and
	// this list must not contradict each other, so what follows is that file's
	// rules restated over the durable projection rather than a second rule:
	//
	//   activeNeedsAttention   -> an occurrence with status running or waiting.
	//   terminalNeedsAttention -> an occurrence with status failed, errored, or
	//                             aborted, unless it is superseded.
	//   superseded             -> ResolveEffective keeps only the LAST terminal
	//                             occurrence per node in the retry closure, and
	//                             suppresses it entirely while a retry is live.
	//                             So: a later terminal occurrence of the same
	//                             node — in a sibling run OR in a later copy of
	//                             an authored retry closure within this same run
	//                             — a live occurrence of that node anywhere in
	//                             the closure, or a retry still in flight.
	//                             Retries are whole-run by construction
	//                             (workflowruns.Handler.Retry rebinds every
	//                             input at the same revision), so an in-flight
	//                             retry is addressing every node of the run it
	//                             retries.
	//
	// An unresolved terminal run whose projection contains no occurrence that
	// ACCOUNTS FOR the failure is treated as a single unresolved unit. That is
	// the run with no projection at all — one predating the table, or whose
	// freeze failed — but it is equally the run whose every node froze pending
	// because it died before any node reported, and the run that failed at the
	// run level with a projection of nothing but successes (an output-contract
	// mismatch). Testing for zero ROWS instead of zero EVIDENCE dropped all but
	// the first from the one surface whose job is answering "is anything
	// unresolved?".
	//
	// There is deliberately no arm admitting a TERMINAL run for carrying a live
	// occurrence. An active run is already admitted by its own status, and a
	// finished run's frozen projection cannot describe work in flight: the
	// reconciler cancels open waits before freezing and occurrence.Derive
	// settles any remaining live status against the run's terminal one. An arm
	// that admitted them anyway pinned finished runs here permanently, because
	// the frozen row is immutable and nothing supersedes a live occurrence.
	needsRetryClosure := false
	switch filter.Lens {
	case AgentWorkflowRunLensActive:
		query += ` AND agent_workflow_runs.status IN (` + placeholders(ActiveAgentWorkflowRunStatuses) + `)`
	case AgentWorkflowRunLensAttention:
		needsRetryClosure = true
		supersedingSibling := func(withNode bool) string {
			clause := `SELECT 1
				FROM agent_workflow_run_retry_closure self
				JOIN agent_workflow_run_retry_closure sibling ON sibling.root = self.root
				JOIN agent_workflow_runs other ON other.id = sibling.id`
			if withNode {
				clause += `
				LEFT JOIN agent_workflow_run_node_occurrences later
					ON later.workflow_run_id = other.id AND later.node_id = o.node_id`
			}
			clause += `
				WHERE self.id = agent_workflow_runs.id
					AND other.id <> agent_workflow_runs.id
					AND (other.status IN (` + placeholders(ActiveAgentWorkflowRunStatuses) + `)`
			if withNode {
				clause += `
						OR later.status IN (` + placeholders(liveNodeOccurrenceStatuses) + `)
						OR (later.status IN (` + placeholders(terminalNodeOccurrenceStatuses) + `)
							AND (other.created_at, other.id) > (agent_workflow_runs.created_at, agent_workflow_runs.id))`
			} else {
				clause += `
						OR (other.status IN (` + placeholders(terminalAgentWorkflowRunStatuses) + `)
							AND (other.created_at, other.id) > (agent_workflow_runs.created_at, agent_workflow_runs.id))`
			}
			return clause + `)`
		}
		// An authored `attempts:` closure puts several copies of one node in ONE
		// run, and ResolveEffective buckets purely by node identity — it does not
		// care which run or which plan copy an entry came from. A lens that only
		// ever let a SIBLING RUN supersede therefore listed a run whose retried
		// node had already succeeded, contradicting the canvas for every retried
		// node. The attempt axes carry the order; plan_id does not.
		supersedingCopy := `SELECT 1
			FROM agent_workflow_run_node_occurrences later
			WHERE later.workflow_run_id = agent_workflow_runs.id
				AND later.node_id = o.node_id
				AND (
					later.status IN (` + placeholders(liveNodeOccurrenceStatuses) + `)
					OR (later.status IN (` + placeholders(terminalNodeOccurrenceStatuses) + `)
						AND (later.retry_attempt, later.attempt) > (o.retry_attempt, o.attempt))
				)`
		unresolvedOccurrence := `SELECT 1 FROM agent_workflow_run_node_occurrences o
				WHERE o.workflow_run_id = agent_workflow_runs.id
					AND o.status IN (` + placeholders(UnresolvedAgentWorkflowRunStatuses) + `)
					AND NOT EXISTS (` + supersedingSibling(true) + `)
					AND NOT EXISTS (` + supersedingCopy + `)`
		query += `
			AND (
				agent_workflow_runs.status IN (` + placeholders(ActiveAgentWorkflowRunStatuses) + `)
				OR EXISTS (` + unresolvedOccurrence + `)
				OR (
					agent_workflow_runs.status IN (` + placeholders(UnresolvedAgentWorkflowRunStatuses) + `)
					AND NOT EXISTS (` + unresolvedOccurrence + `)
					AND NOT EXISTS (` + supersedingSibling(false) + `)
				)
			)`
	}
	// Search is indexed by construction: an exact durable run ID, an exact
	// snapshot ID through agent_workflow_run_snapshots (snapshot_id,
	// workflow_run_id, direction), or a ticket reference prefix. Unbounded JSON
	// scanning is prohibited.
	if search := strings.TrimSpace(filter.Search); search != "" {
		if searchID, err := strconv.ParseInt(search, 10, 64); err == nil {
			args = append(args, searchID)
			placeholder := `$` + strconv.Itoa(len(args))
			query += ` AND (id = ` + placeholder + ` OR EXISTS (
				SELECT 1 FROM agent_workflow_run_snapshots s
				WHERE s.workflow_run_id = agent_workflow_runs.id AND s.snapshot_id = ` + placeholder + `))`
		} else {
			args = append(args, search+"%")
			query += ` AND ticket_reference LIKE $` + strconv.Itoa(len(args))
		}
	}
	if filter.Before != nil {
		args = append(args, filter.Before.CreatedAt.UTC(), filter.Before.ID)
		query += ` AND (created_at, id) < ($` + strconv.Itoa(len(args)-1) + `, $` + strconv.Itoa(len(args)) + `)`
	}
	limit := filter.Limit
	if limit == 0 {
		limit = 100
	}
	args = append(args, limit)
	query += ` ORDER BY created_at DESC, id DESC LIMIT $` + strconv.Itoa(len(args))
	if needsRetryClosure {
		query = agentWorkflowRunRetryClosureCTE(workflowNamePlaceholder) + query
	}
	return queryWorkflowRuns(ctx, factory.conn, factory.conn.EncryptionStrategy(), query, args...)
}

// liveNodeOccurrenceStatuses mirrors occurrence.activeNeedsAttention: the
// in-flight node states a human should be looking at. Pending is deliberately
// absent — every run projects a pending occurrence for every node it never
// reached, and treating no-data as a call to action would drown the lens.
var liveNodeOccurrenceStatuses = []string{"running", "waiting"}

// terminalNodeOccurrenceStatuses mirrors occurrence.Status.Terminal().
var terminalNodeOccurrenceStatuses = []string{
	"succeeded", "failed", "errored", "aborted", "skipped",
}

// terminalAgentWorkflowRunStatuses is the run-level terminal set, matching
// isTerminalWorkflowRunStatus.
var terminalAgentWorkflowRunStatuses = []string{
	string(AgentWorkflowRunStatusSucceeded),
	string(AgentWorkflowRunStatusFailed),
	string(AgentWorkflowRunStatusErrored),
	string(AgentWorkflowRunStatusAborted),
}

// agentWorkflowRunRetryClosureCTE labels every run in scope with the root of
// its retry closure.
//
// retry_of_workflow_run_id is a forest by construction — a run has at most one
// parent and the parent must already exist when the child is admitted — so
// walking down from the roots terminates without cycle bookkeeping. A run whose
// parent falls outside the scoped set seeds its own closure rather than
// disappearing, so narrowing the scope can never silently drop a run from the
// lens.
func agentWorkflowRunRetryClosureCTE(workflowNamePlaceholder string) string {
	scoped := func(alias string) string {
		clause := alias + `.team_id = $1 AND ` + alias + `.definition_kind = $2`
		if workflowNamePlaceholder != "" {
			clause += ` AND ` + alias + `.workflow_name = ` + workflowNamePlaceholder
		}
		return clause
	}
	return `WITH RECURSIVE agent_workflow_run_retry_closure(id, root) AS (
		SELECT r.id, r.id
			FROM agent_workflow_runs r
			WHERE ` + scoped("r") + `
				AND (
					r.retry_of_workflow_run_id IS NULL
					OR NOT EXISTS (
						SELECT 1 FROM agent_workflow_runs p
						WHERE p.id = r.retry_of_workflow_run_id AND ` + scoped("p") + `
					)
				)
		UNION ALL
		SELECT child.id, closure.root
			FROM agent_workflow_runs child
			JOIN agent_workflow_run_retry_closure closure
				ON child.retry_of_workflow_run_id = closure.id
			WHERE ` + scoped("child") + `
	)
	`
}

func (factory *agentWorkflowRunsFactory) CountByStatus(
	ctx context.Context,
	filter AgentWorkflowRunCountFilter,
) (map[AgentWorkflowRunStatus]int64, error) {
	if filter.TeamID <= 0 || strings.TrimSpace(filter.WorkflowName) == "" ||
		filter.ExcludeOriginKind != "" && strings.TrimSpace(filter.ExcludeOriginKind) == "" {
		return nil, fmt.Errorf("db: workflow-run status counts require a team, workflow, and valid excluded origin")
	}
	query := `
		SELECT status, count(*)
		FROM agent_workflow_runs
		WHERE team_id = $1 AND definition_kind = 'workflow' AND workflow_name = $2
	`
	args := []any{filter.TeamID, filter.WorkflowName}
	if filter.ExcludeOriginKind != "" {
		args = append(args, filter.ExcludeOriginKind)
		query += ` AND origin_kind <> $3`
	}
	query += ` GROUP BY status ORDER BY status`
	rows, err := factory.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	counts := make(map[AgentWorkflowRunStatus]int64)
	for rows.Next() {
		var rawStatus string
		var count int64
		if err := rows.Scan(&rawStatus, &count); err != nil {
			return nil, err
		}
		status := AgentWorkflowRunStatus(rawStatus)
		if err := status.Validate(); err != nil || count < 0 {
			return nil, fmt.Errorf("db: invalid workflow-run status count")
		}
		counts[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return counts, nil
}

func (factory *agentWorkflowRunsFactory) LinkExecution(
	ctx context.Context,
	id snapshot.WorkflowRunID,
	link AgentWorkflowRunExecutionLink,
) error {
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if err := linkAgentWorkflowRunExecution(ctx, tx, id, link); err != nil {
		return err
	}
	return tx.Commit()
}

// linkAgentWorkflowRunExecution accepts a transaction so pipeline-run
// creation can attach durable ownership before the new execution is visible.
func linkAgentWorkflowRunExecution(
	ctx context.Context,
	queryer snapshotQueryer,
	id snapshot.WorkflowRunID,
	link AgentWorkflowRunExecutionLink,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := link.Validate(); err != nil {
		return err
	}
	var runTeamID, actualTemplateID, actualInstanceID, templateTeamID, instanceTeamID int
	err := queryer.QueryRowContext(ctx, `
		SELECT r.team_id, pr.template_pipeline_id, pr.instance_pipeline_id,
		       template.team_id, instance.team_id
		FROM agent_workflow_runs r
		JOIN pipeline_runs pr ON pr.id = $2
		JOIN pipelines template ON template.id = pr.template_pipeline_id
		JOIN pipelines instance ON instance.id = pr.instance_pipeline_id
		WHERE r.id = $1
		FOR UPDATE OF r, pr, template, instance
	`, int64(id), link.PipelineRunID).Scan(
		&runTeamID, &actualTemplateID, &actualInstanceID, &templateTeamID, &instanceTeamID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run or pipeline-run execution association is absent")
	}
	if err != nil {
		return err
	}
	if actualTemplateID != link.TemplatePipelineID || actualInstanceID != link.InstancePipelineID {
		return fmt.Errorf("db: workflow-run pipeline execution association does not match its pipeline run")
	}
	if templateTeamID != runTeamID || instanceTeamID != runTeamID {
		return fmt.Errorf("db: workflow-run pipeline execution is not owned by its team")
	}
	var ownerID int64
	err = queryer.QueryRowContext(ctx, `
		SELECT id
		FROM agent_workflow_runs
		WHERE id <> $1
		  AND (pipeline_run_id = $2 OR instance_pipeline_id = $3)
		FOR UPDATE
	`, int64(id), link.PipelineRunID, link.InstancePipelineID).Scan(&ownerID)
	if err == nil {
		return fmt.Errorf("db: workflow-run pipeline execution is already owned by another run")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var linkedID int64
	err = queryer.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET pipeline_run_id = $2,
		    template_pipeline_id = $3,
		    instance_pipeline_id = $4,
		    concrete_config = $5,
		    concrete_config_hash = $6,
		    updated_at = now()
		WHERE id = $1
		  AND (
			(pipeline_run_id IS NULL AND template_pipeline_id IS NULL
			 AND instance_pipeline_id IS NULL AND concrete_config IS NULL
			 AND concrete_config_hash IS NULL)
			OR
			(pipeline_run_id = $2 AND template_pipeline_id = $3
			 AND instance_pipeline_id = $4 AND concrete_config = $5::jsonb
			 AND concrete_config_hash = $6)
		  )
		RETURNING id
	`, int64(id), link.PipelineRunID, link.TemplatePipelineID, link.InstancePipelineID,
		[]byte(link.ConcreteConfig), link.ConcreteConfigHash).Scan(&linkedID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run execution link is absent or conflicts with immutable provenance")
	}
	return err
}

func (factory *agentWorkflowRunsFactory) RecordPlan(
	ctx context.Context,
	id snapshot.WorkflowRunID,
	plan AgentWorkflowRunPlan,
) error {
	if err := id.Validate(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if err := captureAgentWorkflowRunPlan(ctx, tx, id, plan); err != nil {
		return err
	}
	return tx.Commit()
}

type agentWorkflowRunBuildAssociation struct {
	runID                snapshot.WorkflowRunID
	workflowDefinitionID int
	workflowVersion      int
	concreteConfig       json.RawMessage
	status               AgentWorkflowRunStatus
	executionStatus      *AgentWorkflowRunExecutionStatus
	plannedBuildID       *int64
	buildStatus          BuildStatus
	planCaptured         bool
}

func lockAgentWorkflowRunForBuild(
	ctx context.Context,
	tx Tx,
	buildID int64,
) (agentWorkflowRunBuildAssociation, bool, error) {
	if buildID <= 0 {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: workflow-run build ID must be positive")
	}
	var (
		association      agentWorkflowRunBuildAssociation
		runID            int64
		concreteConfig   []byte
		status           string
		executionStatus  sql.NullString
		plannedBuildID   sql.NullInt64
		runTeamID        int
		pipelineRunID    sql.NullInt64
		templatePipeline sql.NullInt64
		instancePipeline sql.NullInt64
		buildStatus      string
		buildTeamID      int
		buildPipelineID  sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT r.id, r.workflow_definition_id, r.workflow_version, r.concrete_config, r.status,
		       r.execution_status, r.planned_build_id, r.team_id,
		       r.pipeline_run_id, r.template_pipeline_id, r.instance_pipeline_id,
		       b.status, b.team_id, b.pipeline_id,
		       r.actual_plan IS NOT NULL
		         AND r.actual_plan_hash IS NOT NULL
		         AND r.resolved_dependencies IS NOT NULL
		FROM builds b
		JOIN agent_workflow_runs r ON r.instance_pipeline_id = b.pipeline_id
		WHERE b.id = $1
		FOR UPDATE OF r, b
	`, buildID).Scan(
		&runID, &association.workflowDefinitionID, &association.workflowVersion, &concreteConfig, &status,
		&executionStatus, &plannedBuildID, &runTeamID,
		&pipelineRunID, &templatePipeline, &instancePipeline,
		&buildStatus, &buildTeamID, &buildPipelineID, &association.planCaptured,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return agentWorkflowRunBuildAssociation{}, false, nil
	}
	if err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}
	if !pipelineRunID.Valid || !templatePipeline.Valid || !instancePipeline.Valid ||
		!buildPipelineID.Valid || buildPipelineID.Int64 != instancePipeline.Int64 ||
		runTeamID != buildTeamID || len(concreteConfig) == 0 {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: workflow-run build has an incomplete durable execution chain")
	}
	var locked int
	err = tx.QueryRowContext(ctx, `
		SELECT 1
		FROM pipeline_runs execution
		JOIN pipelines template ON template.id = execution.template_pipeline_id
		JOIN pipelines instance ON instance.id = execution.instance_pipeline_id
		WHERE execution.id = $1
		  AND execution.template_pipeline_id = $2
		  AND execution.instance_pipeline_id = $3
		  AND template.team_id = $4
		  AND instance.team_id = $4
		FOR UPDATE OF execution, template, instance
	`, pipelineRunID.Int64, templatePipeline.Int64, instancePipeline.Int64, runTeamID).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: workflow-run build execution chain is absent or inconsistent")
	}
	if err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}

	association.runID = snapshot.WorkflowRunID(runID)
	association.concreteConfig = cloneJSON(concreteConfig)
	association.status = AgentWorkflowRunStatus(status)
	association.buildStatus = BuildStatus(buildStatus)
	if err := association.runID.Validate(); err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}
	if err := association.status.Validate(); err != nil {
		return agentWorkflowRunBuildAssociation{}, false, err
	}
	if !validAgentWorkflowRunBuildStatus(association.buildStatus) {
		return agentWorkflowRunBuildAssociation{}, false, fmt.Errorf("db: invalid workflow-run build status %q", association.buildStatus)
	}
	if executionStatus.Valid {
		value := AgentWorkflowRunExecutionStatus(executionStatus.String)
		if err := value.Validate(); err != nil {
			return agentWorkflowRunBuildAssociation{}, false, err
		}
		association.executionStatus = &value
	}
	if plannedBuildID.Valid {
		value := plannedBuildID.Int64
		association.plannedBuildID = &value
	}
	return association, true, nil
}

func validAgentWorkflowRunBuildStatus(status BuildStatus) bool {
	switch status {
	case BuildStatusPending, BuildStatusStarted, BuildStatusSucceeded,
		BuildStatusFailed, BuildStatusErrored, BuildStatusAborted:
		return true
	default:
		return false
	}
}

func agentWorkflowRunExecutionStatusForBuild(status BuildStatus) (AgentWorkflowRunExecutionStatus, error) {
	switch status {
	case BuildStatusSucceeded:
		return AgentWorkflowRunExecutionStatusSucceeded, nil
	case BuildStatusFailed:
		return AgentWorkflowRunExecutionStatusFailed, nil
	case BuildStatusErrored:
		return AgentWorkflowRunExecutionStatusErrored, nil
	case BuildStatusAborted:
		return AgentWorkflowRunExecutionStatusAborted, nil
	default:
		return "", fmt.Errorf("db: workflow-run execution status requires a terminal build, got %q", status)
	}
}

func workflowRunBuildCaptureResult(
	association agentWorkflowRunBuildAssociation,
	disposition AgentWorkflowRunBuildDisposition,
) AgentWorkflowRunBuildCaptureResult {
	return AgentWorkflowRunBuildCaptureResult{
		WorkflowRunID:        association.runID,
		WorkflowDefinitionID: association.workflowDefinitionID,
		ConcreteConfig:       cloneJSON(association.concreteConfig),
		Disposition:          disposition,
	}
}

func recordAgentWorkflowRunAnomaly(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	kind agentWorkflowRunAnomalyKind,
	buildID int64,
	status BuildStatus,
) error {
	if err := runID.Validate(); err != nil {
		return err
	}
	if buildID <= 0 || !validAgentWorkflowRunBuildStatus(status) {
		return fmt.Errorf("db: workflow-run anomaly requires a positive build and valid status")
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_run_anomalies
			(workflow_run_id, kind, build_id, build_status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (workflow_run_id, kind, build_id) DO NOTHING
	`, int64(runID), string(kind), buildID, string(status))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 1 {
		return err
	}
	var storedStatus BuildStatus
	err = tx.QueryRowContext(ctx, `
		SELECT build_status
		FROM agent_workflow_run_anomalies
		WHERE workflow_run_id = $1 AND kind = $2 AND build_id = $3
		FOR UPDATE
	`, int64(runID), string(kind), buildID).Scan(&storedStatus)
	if err != nil {
		return err
	}
	if storedStatus != status {
		return fmt.Errorf("db: workflow-run anomaly conflicts with immutable build status %q", storedStatus)
	}
	return nil
}

func prepareAgentWorkflowRunBuildStart(
	ctx context.Context,
	tx Tx,
	buildID int64,
) (AgentWorkflowRunBuildCaptureResult, error) {
	association, found, err := lockAgentWorkflowRunForBuild(ctx, tx, buildID)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if !found {
		return AgentWorkflowRunBuildCaptureResult{Disposition: AgentWorkflowRunBuildDispositionOrdinary}, nil
	}
	selected := association.plannedBuildID != nil && *association.plannedBuildID == buildID
	active := association.status == AgentWorkflowRunStatusAdmitting ||
		association.status == AgentWorkflowRunStatusRunning ||
		association.status == AgentWorkflowRunStatusCanceling
	if !selected || !active {
		if err := recordAgentWorkflowRunAnomaly(
			ctx, tx, association.runID, agentWorkflowRunAnomalyLaterBuildStarted, buildID, association.buildStatus,
		); err != nil {
			return AgentWorkflowRunBuildCaptureResult{}, err
		}
		return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionAnomalous), nil
	}
	if association.status == AgentWorkflowRunStatusCanceling {
		return AgentWorkflowRunBuildCaptureResult{}, &AgentWorkflowRunCancelingError{WorkflowRunID: association.runID}
	}
	if association.buildStatus != BuildStatusStarted {
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf(
			"db: selected workflow-run build must be started before plan capture, got %q", association.buildStatus,
		)
	}
	return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionSelected), nil
}

func captureAgentWorkflowRunPlan(
	ctx context.Context,
	tx Tx,
	runID snapshot.WorkflowRunID,
	plan AgentWorkflowRunPlan,
) error {
	if err := runID.Validate(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	association, found, err := lockAgentWorkflowRunForBuild(ctx, tx, plan.BuildID)
	if err != nil {
		return err
	}
	if !found || association.runID != runID {
		return fmt.Errorf("db: workflow-run or planned build is absent")
	}
	if association.plannedBuildID == nil || *association.plannedBuildID != plan.BuildID {
		return fmt.Errorf("db: workflow-run build is not the preselected execution build")
	}
	if association.status != AgentWorkflowRunStatusAdmitting && association.status != AgentWorkflowRunStatusRunning {
		return fmt.Errorf("db: workflow-run plan cannot be captured while status is %q", association.status)
	}
	if association.buildStatus != BuildStatusStarted {
		return fmt.Errorf("db: workflow-run plan requires its selected build to be started")
	}

	var (
		storedPlan              sql.NullString
		storedPlanNonce         sql.NullString
		storedPlanHash          sql.NullString
		storedPlanHashNonce     sql.NullString
		storedDependencies      sql.NullString
		storedDependenciesNonce sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
		SELECT actual_plan, actual_plan_nonce,
		       actual_plan_hash, actual_plan_hash_nonce,
		       resolved_dependencies, resolved_dependencies_nonce
		FROM agent_workflow_runs
		WHERE id = $1
	`, int64(runID)).Scan(
		&storedPlan, &storedPlanNonce, &storedPlanHash, &storedPlanHashNonce,
		&storedDependencies, &storedDependenciesNonce,
	)
	if err != nil {
		return err
	}

	empty := !storedPlan.Valid && !storedPlanNonce.Valid &&
		!storedPlanHash.Valid && !storedPlanHashNonce.Valid &&
		!storedDependencies.Valid && !storedDependenciesNonce.Valid
	complete := storedPlan.Valid && storedPlanHash.Valid && storedDependencies.Valid
	if !empty {
		if !complete {
			return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
		}
		var planNonce *string
		if storedPlanNonce.Valid {
			planNonce = &storedPlanNonce.String
		}
		plaintextPlan, err := tx.EncryptionStrategy().Decrypt(storedPlan.String, planNonce)
		if err != nil {
			return fmt.Errorf("db: decrypt workflow-run actual plan: %w", err)
		}
		var hashNonce *string
		if storedPlanHashNonce.Valid {
			hashNonce = &storedPlanHashNonce.String
		}
		plaintextHash, err := tx.EncryptionStrategy().Decrypt(storedPlanHash.String, hashNonce)
		if err != nil {
			return fmt.Errorf("db: decrypt workflow-run actual plan hash: %w", err)
		}
		var dependenciesNonce *string
		if storedDependenciesNonce.Valid {
			dependenciesNonce = &storedDependenciesNonce.String
		}
		plaintextDependencies, err := tx.EncryptionStrategy().Decrypt(storedDependencies.String, dependenciesNonce)
		if err != nil {
			return fmt.Errorf("db: decrypt workflow-run resolved dependencies: %w", err)
		}
		if !semanticJSONEqual(plaintextPlan, plan.ActualPlan) ||
			string(plaintextHash) != plan.ActualPlanHash ||
			!semanticJSONEqual(plaintextDependencies, plan.ResolvedDependencies) {
			return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
		}
		return nil
	}

	encryptedPlan, planNonce, err := tx.EncryptionStrategy().Encrypt(plan.ActualPlan)
	if err != nil {
		return fmt.Errorf("db: encrypt workflow-run actual plan: %w", err)
	}
	encryptedPlanHash, planHashNonce, err := tx.EncryptionStrategy().Encrypt([]byte(plan.ActualPlanHash))
	if err != nil {
		return fmt.Errorf("db: encrypt workflow-run actual plan hash: %w", err)
	}
	encryptedDependencies, dependenciesNonce, err := tx.EncryptionStrategy().Encrypt(plan.ResolvedDependencies)
	if err != nil {
		return fmt.Errorf("db: encrypt workflow-run resolved dependencies: %w", err)
	}
	var capturedID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET actual_plan = $3,
		    actual_plan_nonce = $4,
		    actual_plan_hash = $5,
		    actual_plan_hash_nonce = $6,
		    resolved_dependencies = $7,
		    resolved_dependencies_nonce = $8,
		    updated_at = now()
		WHERE id = $1
		  AND planned_build_id = $2
		  AND status IN ('admitting', 'running')
		  AND actual_plan IS NULL
		  AND actual_plan_nonce IS NULL
		  AND actual_plan_hash IS NULL
		  AND actual_plan_hash_nonce IS NULL
		  AND resolved_dependencies IS NULL
		  AND resolved_dependencies_nonce IS NULL
		RETURNING id
	`, int64(runID), plan.BuildID, encryptedPlan, planNonce, encryptedPlanHash,
		planHashNonce, encryptedDependencies, dependenciesNonce).Scan(&capturedID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: workflow-run actual plan is absent or conflicts with immutable provenance")
	}
	return err
}

func (factory *agentWorkflowRunsFactory) CaptureExecutionStatus(
	ctx context.Context,
	buildID int64,
	status BuildStatus,
) (AgentWorkflowRunBuildCaptureResult, error) {
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	defer Rollback(tx)
	result, err := captureAgentWorkflowRunExecutionStatus(ctx, tx, buildID, status)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	return result, nil
}

func captureAgentWorkflowRunExecutionStatus(
	ctx context.Context,
	tx Tx,
	buildID int64,
	status BuildStatus,
) (AgentWorkflowRunBuildCaptureResult, error) {
	executionStatus, err := agentWorkflowRunExecutionStatusForBuild(status)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	association, found, err := lockAgentWorkflowRunForBuild(ctx, tx, buildID)
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if !found {
		return AgentWorkflowRunBuildCaptureResult{Disposition: AgentWorkflowRunBuildDispositionOrdinary}, nil
	}
	if association.buildStatus != status {
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf(
			"db: workflow-run build status %q does not match copied outcome %q", association.buildStatus, status,
		)
	}
	selected := association.plannedBuildID != nil && *association.plannedBuildID == buildID
	if !selected {
		if err := recordAgentWorkflowRunAnomaly(
			ctx, tx, association.runID, agentWorkflowRunAnomalyLaterBuildCompleted, buildID, status,
		); err != nil {
			return AgentWorkflowRunBuildCaptureResult{}, err
		}
		return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionAnomalous), nil
	}
	terminal := association.status == AgentWorkflowRunStatusSucceeded ||
		association.status == AgentWorkflowRunStatusFailed ||
		association.status == AgentWorkflowRunStatusErrored ||
		association.status == AgentWorkflowRunStatusAborted
	if terminal {
		if association.executionStatus != nil && *association.executionStatus == executionStatus {
			return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionSelected), nil
		}
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf(
			"db: selected workflow-run execution outcome conflicts with immutable history",
		)
	}

	var captured int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET execution_status = COALESCE(execution_status, $3),
		    updated_at = CASE WHEN execution_status IS NULL THEN now() ELSE updated_at END
		WHERE id = $1
		  AND planned_build_id = $2
		  AND status IN ('admitting', 'running', 'canceling')
		  AND (execution_status IS NULL OR execution_status = $3)
		RETURNING id
	`, int64(association.runID), buildID, string(executionStatus)).Scan(&captured)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf("db: workflow-run execution outcome conflicts with immutable history")
	}
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	return workflowRunBuildCaptureResult(association, AgentWorkflowRunBuildDispositionSelected), nil
}

func terminalizeAgentWorkflowRunPlanningError(
	ctx context.Context,
	tx Tx,
	buildID int64,
	message string,
) (AgentWorkflowRunBuildCaptureResult, error) {
	result, err := captureAgentWorkflowRunExecutionStatus(ctx, tx, buildID, BuildStatusErrored)
	if err != nil || result.Disposition != AgentWorkflowRunBuildDispositionSelected {
		return result, err
	}
	message = truncateAgentWorkflowRunMessage(message, 4*1024)
	var terminalID int64
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET status = 'errored',
		    error_message = $2,
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE id = $1
		  AND status IN ('admitting', 'running', 'canceling')
		  AND execution_status = 'errored'
		RETURNING id
	`, int64(result.WorkflowRunID), message).Scan(&terminalID)
	if errors.Is(err, sql.ErrNoRows) {
		var status AgentWorkflowRunStatus
		var execution AgentWorkflowRunExecutionStatus
		var storedMessage string
		err = tx.QueryRowContext(ctx, `
			SELECT status, execution_status, error_message
			FROM agent_workflow_runs WHERE id = $1
		`, int64(result.WorkflowRunID)).Scan(&status, &execution, &storedMessage)
		if err == nil && status == AgentWorkflowRunStatusErrored &&
			execution == AgentWorkflowRunExecutionStatusErrored && storedMessage == message {
			if err := releaseAgentWorkflowRunRetention(ctx, tx, result.WorkflowRunID); err != nil {
				return AgentWorkflowRunBuildCaptureResult{}, err
			}
			return result, nil
		}
		if err != nil {
			return AgentWorkflowRunBuildCaptureResult{}, err
		}
		return AgentWorkflowRunBuildCaptureResult{}, fmt.Errorf("db: workflow-run planning error conflicts with immutable terminal history")
	}
	if err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	if err := releaseAgentWorkflowRunRetention(ctx, tx, result.WorkflowRunID); err != nil {
		return AgentWorkflowRunBuildCaptureResult{}, err
	}
	return result, nil
}

func truncateAgentWorkflowRunMessage(message string, limit int) string {
	message = strings.ToValidUTF8(message, "\uFFFD")
	if len(message) <= limit {
		return message
	}
	message = message[:limit]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	return message
}

func (factory *agentWorkflowRunsFactory) Transition(
	ctx context.Context,
	id snapshot.WorkflowRunID,
	from AgentWorkflowRunStatus,
	to AgentWorkflowRunStatus,
	errorMessage string,
) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	if err := from.Validate(); err != nil {
		return false, err
	}
	if err := to.Validate(); err != nil {
		return false, err
	}
	if err := validateAgentWorkflowRunTransition(from, to); err != nil {
		return false, err
	}
	directAdmissionFailure := from == AgentWorkflowRunStatusAdmitting && to == AgentWorkflowRunStatusErrored
	if workflowRunTerminal(to) && !directAdmissionFailure {
		return false, fmt.Errorf("db: terminal workflow-run transitions require locked Finalize evidence")
	}
	if len(errorMessage) > 64*1024 {
		return false, fmt.Errorf("db: workflow-run error message exceeds 64 KiB")
	}
	terminal := to == AgentWorkflowRunStatusSucceeded || to == AgentWorkflowRunStatusFailed ||
		to == AgentWorkflowRunStatusErrored || to == AgentWorkflowRunStatusAborted
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_runs
		SET status = $3,
		    error_message = $4,
		    started_at = CASE WHEN $3 = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
		    completed_at = CASE WHEN $5 THEN COALESCE(completed_at, now()) ELSE completed_at END,
		    updated_at = now()
		WHERE id = $1 AND status = $2
		  AND (
			NOT $6
			OR (
				pipeline_run_id IS NULL
				AND template_pipeline_id IS NULL
				AND instance_pipeline_id IS NULL
				AND concrete_config IS NULL
				AND concrete_config_hash IS NULL
				AND planned_build_id IS NULL
				AND actual_plan IS NULL
				AND actual_plan_hash IS NULL
				AND resolved_dependencies IS NULL
				AND execution_status IS NULL
			)
		  )
	`, int64(id), string(from), string(to), errorMessage, terminal, directAdmissionFailure)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if count == 1 && terminal {
		if err := releaseAgentWorkflowRunRetention(ctx, tx, id); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (factory *agentWorkflowRunsFactory) ClaimForReconciliation(
	ctx context.Context,
	now time.Time,
	delay time.Duration,
	limit int,
) ([]snapshot.WorkflowRunID, error) {
	if now.IsZero() || delay <= 0 || limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("db: workflow-run reconciliation requires a time, positive delay, and limit between 1 and 1000")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	rows, err := tx.QueryContext(ctx, `
		WITH due AS (
			SELECT run.id
			FROM agent_workflow_runs run
			WHERE (
				run.status IN ('admitting', 'running', 'canceling')
				OR (
					-- A wait left open by ANY terminal outcome is orphaned: the
					-- build that would have answered it is gone. Keeping this
					-- arm to 'aborted' alone left a failed or errored run's
					-- waits 'waiting' forever, with nothing due to repair them.
					run.status IN ('succeeded', 'failed', 'errored', 'aborted')
					AND EXISTS (
						SELECT 1
						FROM agent_workflow_waits wait
						WHERE wait.workflow_run_id = run.id
						  AND wait.team_id = run.team_id
						  AND wait.status = 'waiting'
					)
				)
				OR (
					-- Ticket projection happens after the finalization CAS. Keep a
					-- terminal row due only while this exact current dispatch still
					-- needs its ticket moved to needs_review.
					run.status IN ('succeeded', 'failed', 'errored', 'aborted')
					AND EXISTS (
						SELECT 1
						FROM agent_tickets ticket
						WHERE ticket.id = run.ticket_id
						  AND ticket.state = 'running'
						  AND ticket.dispatch_reservation_key <> ''
						  AND ticket.dispatch_reservation_key = run.idempotency_key
					)
				)
			  )
			  AND run.reconcile_after <= $1
			ORDER BY reconcile_after, id
			FOR UPDATE OF run SKIP LOCKED
			LIMIT $3
		)
		UPDATE agent_workflow_runs AS run
		SET reconcile_after = $2
		FROM due
		WHERE run.id = due.id
		RETURNING run.id
	`, now, now.Add(delay), limit)
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	ids := make([]snapshot.WorkflowRunID, 0, limit)
	for rows.Next() {
		var rawID int64
		if err := rows.Scan(&rawID); err != nil {
			return nil, err
		}
		id := snapshot.WorkflowRunID(rawID)
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("db: invalid claimed workflow-run ID: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

func (factory *agentWorkflowRunsFactory) InspectForReconciliation(
	ctx context.Context,
	id snapshot.WorkflowRunID,
) (AgentWorkflowRunReconciliationView, bool, error) {
	if err := id.Validate(); err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	defer Rollback(tx)
	run, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1
		FOR SHARE
	`, int64(id)), tx.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunReconciliationView{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	view := AgentWorkflowRunReconciliationView{Run: run}
	if run.PlannedBuildID != nil {
		var status string
		err = tx.QueryRowContext(ctx, `SELECT status FROM builds WHERE id = $1`, *run.PlannedBuildID).Scan(&status)
		if err == nil {
			view.SelectedBuildExists = true
			view.SelectedBuildStatus = BuildStatus(status)
			if !validAgentWorkflowRunBuildStatus(view.SelectedBuildStatus) {
				return AgentWorkflowRunReconciliationView{}, false, fmt.Errorf("db: invalid selected workflow-run build status %q", status)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return AgentWorkflowRunReconciliationView{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowRunReconciliationView{}, false, err
	}
	return view, true, nil
}

func (factory *agentWorkflowRunsFactory) AdvanceAdmission(
	ctx context.Context,
	id snapshot.WorkflowRunID,
) (bool, error) {
	if err := id.Validate(); err != nil {
		return false, err
	}
	// CreateRunForWorkflowRun copies this complete link while holding the run
	// row in the same transaction that creates and selects the entry build.
	// Those copied scalars are the durable admission evidence: pipeline, build,
	// and template rows are deliberately allowed to be garbage-collected before
	// a crashed web process resumes this CAS.
	result, err := factory.conn.ExecContext(ctx, `
		UPDATE agent_workflow_runs AS run
		SET status = 'running',
		    started_at = COALESCE(started_at, now()),
		    updated_at = now()
		WHERE run.id = $1
		  AND run.status = 'admitting'
		  AND run.pipeline_run_id IS NOT NULL
		  AND run.template_pipeline_id IS NOT NULL
		  AND run.instance_pipeline_id IS NOT NULL
		  AND run.concrete_config IS NOT NULL
		  AND run.concrete_config_hash IS NOT NULL
		  AND run.planned_build_id IS NOT NULL
	`, int64(id))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (factory *agentWorkflowRunsFactory) Finalize(
	ctx context.Context,
	finalization AgentWorkflowRunFinalization,
) (AgentWorkflowRunFinalizationResult, bool, error) {
	if err := finalization.Validate(); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	defer Rollback(tx)
	run, err := scanAgentWorkflowRun(tx.QueryRowContext(ctx, `
		SELECT `+agentWorkflowRunColumns+`
		FROM agent_workflow_runs
		WHERE id = $1
		FOR UPDATE
	`, int64(finalization.WorkflowRunID)), tx.EncryptionStrategy())
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunFinalizationResult{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	if workflowRunTerminal(run.Status) {
		if err := releaseAgentWorkflowRunRetention(ctx, tx, finalization.WorkflowRunID); err != nil {
			return AgentWorkflowRunFinalizationResult{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return AgentWorkflowRunFinalizationResult{}, false, err
		}
		return workflowRunFinalizationResult(run), false, nil
	}
	if run.Status != finalization.ExpectedStatus ||
		!equalAgentWorkflowRunExecutionStatus(run.ExecutionStatus, finalization.ExpectedExecutionStatus) ||
		!equalOptionalString(run.ActualPlanHash, finalization.ExpectedActualPlanHash) {
		return AgentWorkflowRunFinalizationResult{}, false, nil
	}

	terminalStatus := finalization.TerminalStatus
	errorMessage := finalization.ErrorMessage
	if terminalStatus == AgentWorkflowRunStatusSucceeded {
		terminalStatus, errorMessage, err = validateAgentWorkflowRunOutputEvidence(
			ctx, tx, run, finalization.ExpectedOutputs,
		)
		if err != nil {
			return AgentWorkflowRunFinalizationResult{}, false, err
		}
	}
	if err := validateAgentWorkflowRunTransition(run.Status, terminalStatus); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	errorMessage = truncateAgentWorkflowRunMessage(errorMessage, 4*1024)
	if terminalStatus == AgentWorkflowRunStatusSucceeded {
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_workflow_run_snapshots
			SET promoted_at = COALESCE(promoted_at, now())
			WHERE workflow_run_id = $1 AND direction = 'output'
		`, int64(finalization.WorkflowRunID)); err != nil {
			return AgentWorkflowRunFinalizationResult{}, false, err
		}
	}

	var completedAt time.Time
	err = tx.QueryRowContext(ctx, `
		UPDATE agent_workflow_runs
		SET status = $5,
		    error_message = $6,
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE id = $1
		  AND status = $2
		  AND execution_status IS NOT DISTINCT FROM $3
		  AND (actual_plan_hash IS NOT NULL) = $4
		RETURNING completed_at
	`, int64(finalization.WorkflowRunID), string(finalization.ExpectedStatus),
		optionalAgentWorkflowRunExecutionStatus(finalization.ExpectedExecutionStatus),
		finalization.ExpectedActualPlanHash != nil, string(terminalStatus), errorMessage).Scan(&completedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowRunFinalizationResult{}, false, nil
	}
	if err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	if err := releaseAgentWorkflowRunRetention(ctx, tx, finalization.WorkflowRunID); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowRunFinalizationResult{}, false, err
	}
	return AgentWorkflowRunFinalizationResult{
		Status: terminalStatus, ErrorMessage: errorMessage, CompletedAt: completedAt,
	}, true, nil
}

func releaseAgentWorkflowRunRetention(
	ctx context.Context,
	tx Tx,
	workflowRunID snapshot.WorkflowRunID,
) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM agent_snapshot_retention_claims
		WHERE class = 'run' AND workflow_run_id = $1
	`, int64(workflowRunID))
	return err
}

func workflowRunTerminal(status AgentWorkflowRunStatus) bool {
	return status == AgentWorkflowRunStatusSucceeded || status == AgentWorkflowRunStatusFailed ||
		status == AgentWorkflowRunStatusErrored || status == AgentWorkflowRunStatusAborted
}

func workflowRunFinalizationResult(run AgentWorkflowRun) AgentWorkflowRunFinalizationResult {
	result := AgentWorkflowRunFinalizationResult{Status: run.Status, ErrorMessage: run.ErrorMessage}
	if run.CompletedAt != nil {
		result.CompletedAt = *run.CompletedAt
	}
	return result
}

func equalAgentWorkflowRunExecutionStatus(left, right *AgentWorkflowRunExecutionStatus) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func optionalAgentWorkflowRunExecutionStatus(status *AgentWorkflowRunExecutionStatus) any {
	if status == nil {
		return nil
	}
	return string(*status)
}

type agentWorkflowRunOutputBinding struct {
	snapshotID   snapshot.SnapshotID
	typeRef      snapshot.TypeRef
	digest       snapshot.Digest
	contentState snapshot.ContentState
}

func validateAgentWorkflowRunOutputEvidence(
	ctx context.Context,
	tx Tx,
	run AgentWorkflowRun,
	expected []AgentWorkflowRunExpectedOutput,
) (AgentWorkflowRunStatus, string, error) {
	expectedByPort := make(map[string]AgentWorkflowRunExpectedOutput, len(expected))
	for _, output := range expected {
		expectedByPort[output.Port] = output
		if agentWorkflowRunOutputHasAmbiguousProducer(output) {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has ambiguous producer evidence", output.Port), nil
		}
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT binding.port_name, value.id, value.type_name, value.type_version,
		       value.digest, value.content_state
		FROM agent_workflow_run_snapshots binding
		JOIN agent_snapshots value ON value.id = binding.snapshot_id
		WHERE binding.workflow_run_id = $1
		  AND binding.direction = 'output'
		ORDER BY binding.port_name
	`, int64(run.ID))
	if err != nil {
		return "", "", err
	}
	bindings := make(map[string]agentWorkflowRunOutputBinding)
	for rows.Next() {
		var (
			port, typeName, digest, contentState string
			snapshotID                           int64
			typeVersion                          int
		)
		if err := rows.Scan(&port, &snapshotID, &typeName, &typeVersion, &digest, &contentState); err != nil {
			Close(rows)
			return "", "", err
		}
		typeRef, err := joinSnapshotType(typeName, typeVersion)
		if err != nil {
			Close(rows)
			return "", "", err
		}
		bindings[port] = agentWorkflowRunOutputBinding{
			snapshotID: snapshot.SnapshotID(snapshotID), typeRef: typeRef,
			digest: snapshot.Digest(digest), contentState: snapshot.ContentState(contentState),
		}
	}
	err = rows.Err()
	Close(rows)
	if err != nil {
		return "", "", err
	}
	for port := range bindings {
		if _, found := expectedByPort[port]; !found {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("unexpected workflow output %q", port), nil
		}
	}
	for _, output := range expected {
		if output.WorkflowDefinitionID != run.WorkflowDefinitionID || output.WorkflowRunID != run.ID {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has mismatched workflow identity", output.Port), nil
		}
		binding, found := bindings[output.Port]
		if !found {
			if output.Optional {
				continue
			}
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("required output %q is missing", output.Port), nil
		}
		if binding.typeRef != output.Type {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has the wrong type", output.Port), nil
		}
		if run.PlannedBuildID == nil {
			return AgentWorkflowRunStatusErrored, "workflow output provenance has no selected build", nil
		}
		matched, err := agentWorkflowRunOutputHasMatchingProduction(ctx, tx, run, binding, output)
		if err != nil {
			return "", "", err
		}
		if !matched {
			return AgentWorkflowRunStatusFailed, fmt.Sprintf("workflow output %q has no matching producer evidence", output.Port), nil
		}
		if binding.contentState != snapshot.ContentStateAvailable {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q content is not available", output.Port), nil
		}
		var owned bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM agent_snapshots
				WHERE id = $1 AND team_id = $2
			)
		`, int64(binding.snapshotID), run.TeamID).Scan(&owned); err != nil {
			return "", "", err
		}
		if !owned {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q is not owned by the workflow team", output.Port), nil
		}
		actor := fmt.Sprintf("workflow-run:%d:output:%s", int64(run.ID), output.Port)
		var retained bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM agent_snapshot_retention_claims
				WHERE snapshot_id = $1 AND team_id = $2
				  AND class = 'workflow' AND expires_at IS NULL AND actor = $3
			)
		`, int64(binding.snapshotID), run.TeamID, actor).Scan(&retained); err != nil {
			return "", "", err
		}
		if !retained {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q has no permanent workflow claim", output.Port), nil
		}
		var located bool
		if err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM agent_snapshot_locations WHERE digest = $1)
		`, binding.digest.String()).Scan(&located); err != nil {
			return "", "", err
		}
		if !located {
			return AgentWorkflowRunStatusErrored, fmt.Sprintf("workflow output %q has no durable location", output.Port), nil
		}
	}
	return AgentWorkflowRunStatusSucceeded, "", nil
}

func agentWorkflowRunOutputHasMatchingProduction(
	ctx context.Context,
	tx Tx,
	run AgentWorkflowRun,
	binding agentWorkflowRunOutputBinding,
	output AgentWorkflowRunExpectedOutput,
) (bool, error) {
	productionRows, err := tx.QueryContext(ctx, `
		SELECT plan_id, step_kind, step_name, output_port
		FROM agent_snapshot_productions
		WHERE occurrence_kind = 'build'
		  AND snapshot_id = $1
		  AND team_id = $2
		  AND workflow_run_id = $3
		  AND workflow_definition_id = $4
		  AND build_id = $5
		ORDER BY id
	`, int64(binding.snapshotID), run.TeamID, int64(run.ID), run.WorkflowDefinitionID, *run.PlannedBuildID)
	if err != nil {
		return false, err
	}
	defer Close(productionRows)
	matched := false
	for productionRows.Next() {
		var producer AgentWorkflowRunExpectedProducer
		if err := productionRows.Scan(
			&producer.PlanID, &producer.StepKind, &producer.StepName, &producer.LocalOutputPort,
		); err != nil {
			return false, err
		}
		for _, allowed := range output.Producers {
			if producer == allowed {
				matched = true
			}
		}
	}
	return matched, productionRows.Err()
}

func agentWorkflowRunOutputHasAmbiguousProducer(output AgentWorkflowRunExpectedOutput) bool {
	if len(output.Producers) < 2 {
		return false
	}
	logical := output.Producers[0]
	for _, producer := range output.Producers[1:] {
		if producer.StepKind != logical.StepKind || producer.StepName != logical.StepName ||
			producer.LocalOutputPort != logical.LocalOutputPort {
			return true
		}
	}
	return false
}

func queryWorkflowRuns(
	ctx context.Context,
	queryer snapshotQueryer,
	encryptionStrategy encryption.Strategy,
	query string,
	args ...any,
) ([]AgentWorkflowRun, error) {
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	runs := make([]AgentWorkflowRun, 0)
	for rows.Next() {
		run, err := scanAgentWorkflowRun(rows, encryptionStrategy)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
}

func (factory *agentWorkflowRunsFactory) Snapshots(
	ctx context.Context,
	runID snapshot.WorkflowRunID,
) ([]AgentWorkflowRunSnapshotBinding, error) {
	if err := runID.Validate(); err != nil {
		return nil, err
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT b.direction, b.port_name,
		       s.id, s.type_name, s.type_version, s.digest
		FROM agent_workflow_run_snapshots b
		JOIN agent_snapshots s ON s.id = b.snapshot_id
		WHERE b.workflow_run_id = $1
		  AND (b.direction = 'input' OR b.promoted_at IS NOT NULL)
		ORDER BY b.direction, b.port_name
	`, int64(runID))
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	bindings := make([]AgentWorkflowRunSnapshotBinding, 0)
	for rows.Next() {
		var direction, port, typeName, digest string
		var id int64
		var typeVersion int
		if err := rows.Scan(&direction, &port, &id, &typeName, &typeVersion, &digest); err != nil {
			return nil, err
		}
		typeRef, err := joinSnapshotType(typeName, typeVersion)
		if err != nil {
			return nil, err
		}
		binding := AgentWorkflowRunSnapshotBinding{
			WorkflowRunID: runID,
			Direction:     AgentWorkflowRunSnapshotDirection(direction),
			PortName:      port,
			Snapshot: snapshot.SnapshotRef{
				ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest),
			},
		}
		if err := binding.Direction.Validate(); err != nil {
			return nil, err
		}
		if err := binding.Snapshot.Validate(); err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

// InputBindingMatches authoritatively verifies both the current workflow
// execution/build linkage and one exact immutable input binding in a single
// database read. A nil ref means the named input port must be unbound.
func (factory *agentWorkflowRunsFactory) InputBindingMatches(
	ctx context.Context,
	teamID int,
	buildID int,
	runID snapshot.WorkflowRunID,
	port string,
	ref *snapshot.SnapshotRef,
) (bool, error) {
	if teamID <= 0 || buildID <= 0 || strings.TrimSpace(port) == "" {
		return false, fmt.Errorf("db: workflow input binding requires positive team/build IDs and a port")
	}
	if err := runID.Validate(); err != nil {
		return false, err
	}

	var snapshotID int64
	var typeName, digest string
	var typeVersion int
	hasRef := ref != nil
	if ref != nil {
		if err := ref.Validate(); err != nil {
			return false, err
		}
		var err error
		typeName, typeVersion, err = splitSnapshotType(ref.Type)
		if err != nil {
			return false, err
		}
		snapshotID = int64(ref.ID)
		digest = ref.Digest.String()
	}

	var matches bool
	err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM agent_workflow_runs r
			JOIN pipeline_runs execution
			  ON execution.id = r.pipeline_run_id
			 AND execution.template_pipeline_id = r.template_pipeline_id
			 AND execution.instance_pipeline_id = r.instance_pipeline_id
			JOIN pipelines template ON template.id = r.template_pipeline_id
			JOIN pipelines instance ON instance.id = r.instance_pipeline_id
			JOIN builds build ON build.id = $2
			WHERE r.id = $3
			  AND r.team_id = $1
			  AND r.planned_build_id = $2
			  AND template.team_id = $1
			  AND instance.team_id = $1
			  AND build.team_id = $1
			  AND build.pipeline_id = instance.id
			  AND (
				($6 AND EXISTS (
					SELECT 1
					FROM agent_workflow_run_snapshots binding
					JOIN agent_snapshots value ON value.id = binding.snapshot_id
					WHERE binding.workflow_run_id = r.id
					  AND binding.direction = 'input'
					  AND binding.port_name = $4
					  AND binding.snapshot_id = $5
					  AND value.type_name = $7
					  AND value.type_version = $8
					  AND value.digest = $9
				))
				OR
				(NOT $6 AND NOT EXISTS (
					SELECT 1
					FROM agent_workflow_run_snapshots binding
					WHERE binding.workflow_run_id = r.id
					  AND binding.direction = 'input'
					  AND binding.port_name = $4
				))
			  )
		)
	`, teamID, buildID, int64(runID), port, snapshotID, hasRef, typeName, typeVersion, digest).Scan(&matches)
	return matches, err
}
