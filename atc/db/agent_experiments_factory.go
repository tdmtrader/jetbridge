package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/experiment"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/snapshot/contracts"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

const agentExperimentLeaseSeconds = 60

//counterfeiter:generate . AgentExperimentsFactory
type AgentExperimentsFactory interface {
	experiment.Store
	experiment.PagedStore
	experiment.RunnerStore
	experiment.EvaluationStore
	experiment.EvaluationMeasurementsStore
	experiment.CancellationStore
	experiment.BudgetController
}

type AgentExperimentBudgetConfig struct {
	GlobalDailyCapUSD float64
	Location          *time.Location
	Now               func() time.Time
}

type AgentExperimentsFactoryOption func(*agentExperimentsFactory)

func WithAgentExperimentBudgetConfig(config AgentExperimentBudgetConfig) AgentExperimentsFactoryOption {
	return func(factory *agentExperimentsFactory) { factory.budgetConfig = config }
}

func WithAgentExperimentResourceSourcePreparer(
	preparer experiment.ResourceSourcePreparer,
) AgentExperimentsFactoryOption {
	return func(factory *agentExperimentsFactory) {
		factory.resourceSourcePreparer = preparer
	}
}

func NewAgentExperimentsFactory(
	conn DbConn,
	targetRenderer experiment.TargetRenderer,
	options ...AgentExperimentsFactoryOption,
) AgentExperimentsFactory {
	factory := &agentExperimentsFactory{
		conn: conn, targetRenderer: targetRenderer,
		budgetConfig: AgentExperimentBudgetConfig{Location: time.Local, Now: time.Now},
	}
	for _, option := range options {
		if option != nil {
			option(factory)
		}
	}
	if factory.budgetConfig.Location == nil {
		factory.budgetConfig.Location = time.Local
	}
	if factory.budgetConfig.Now == nil {
		factory.budgetConfig.Now = time.Now
	}
	return factory
}

type agentExperimentsFactory struct {
	conn                   DbConn
	targetRenderer         experiment.TargetRenderer
	resourceSourcePreparer experiment.ResourceSourcePreparer
	budgetConfig           AgentExperimentBudgetConfig
}

var (
	_ experiment.Store                       = (*agentExperimentsFactory)(nil)
	_ experiment.RunnerStore                 = (*agentExperimentsFactory)(nil)
	_ experiment.EvaluationStore             = (*agentExperimentsFactory)(nil)
	_ experiment.EvaluationMeasurementsStore = (*agentExperimentsFactory)(nil)
	_ experiment.CancellationStore           = (*agentExperimentsFactory)(nil)
	_ experiment.BudgetController            = (*agentExperimentsFactory)(nil)
)

type agentExperimentQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) squirrel.RowScanner
}

func (factory *agentExperimentsFactory) Create(
	ctx context.Context,
	teamID int,
	teamName string,
	actor string,
	definition experiment.Definition,
) (experiment.StoredExperiment, error) {
	if err := validateExperimentMutation(ctx, teamID, teamName, actor, definition); err != nil {
		return experiment.StoredExperiment{}, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	defer Rollback(tx)
	if err := validateAuthoritativeExperimentTargets(ctx, tx, definition); err != nil {
		return experiment.StoredExperiment{}, err
	}

	candidateSignature, evaluatorSignature, err := marshalExperimentSignatures(definition)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	evaluatorNodeParameters, err := marshalExperimentNodeParameters(definition.Evaluator.Target)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	var id, createdRevision int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO agent_experiments
			(team_id, team_name, name, state, candidate_signature, repetitions,
			 per_cell_budget_usd, total_budget_usd, max_tokens_per_cell,
			 evaluator_target_kind, evaluator_workflow_name, evaluator_definition_id,
			 evaluator_workflow_version, evaluator_function_id, evaluator_node_parameters,
			 evaluator_signature, evaluator_measurements_port, created_by)
		SELECT t.id, t.name, $3, 'draft', $4, $5, $6, $7, $8,
		       $9, $10, $11, $12, $13, $14, $15, $16, $17
		FROM teams t
		WHERE t.id = $1 AND t.name = $2
		RETURNING id, revision
	`, teamID, teamName, definition.Name, candidateSignature, definition.Repetitions,
		definition.Budget.PerCellUSD, definition.Budget.TotalUSD, definition.Budget.MaxTokensPerCell,
		string(definition.Evaluator.Target.Kind), definition.Evaluator.Target.WorkflowName,
		definition.Evaluator.Target.DefinitionID, definition.Evaluator.Target.Version,
		nullableString(definition.Evaluator.Target.FunctionID), evaluatorNodeParameters,
		evaluatorSignature, definition.Evaluator.MeasurementsPort, actor).Scan(&id, &createdRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return experiment.StoredExperiment{}, experiment.ErrNotFound
	}
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := insertExperimentDefinition(ctx, tx, experiment.ID(id), teamID, definition); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := appendExperimentAuditEvent(
		ctx,
		tx,
		teamID,
		experiment.ID(id),
		experiment.AuditCreate,
		actor,
		createdRevision,
	); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := tx.Commit(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	stored, found, err := factory.Get(ctx, teamID, experiment.ID(id))
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if !found {
		return experiment.StoredExperiment{}, experiment.ErrNotFound
	}
	return stored, nil
}

func (factory *agentExperimentsFactory) Update(
	ctx context.Context,
	teamID int,
	id experiment.ID,
	revision int64,
	actor string,
	definition experiment.Definition,
) (experiment.StoredExperiment, error) {
	if err := validateExperimentMutation(ctx, teamID, "trusted-team", actor, definition); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := id.Validate(); err != nil || revision <= 0 {
		return experiment.StoredExperiment{}, experiment.ErrInvalidDefinition
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	defer Rollback(tx)

	state, currentRevision, err := lockExperiment(ctx, tx, teamID, id)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if state != experiment.StateDraft {
		return experiment.StoredExperiment{}, experiment.ErrImmutable
	}
	if currentRevision != revision {
		return experiment.StoredExperiment{}, experiment.ErrRevisionConflict
	}
	if err := validateAuthoritativeExperimentTargets(ctx, tx, definition); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := deleteDraftExperimentDefinition(ctx, tx, id); err != nil {
		return experiment.StoredExperiment{}, err
	}
	candidateSignature, evaluatorSignature, err := marshalExperimentSignatures(definition)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	evaluatorNodeParameters, err := marshalExperimentNodeParameters(definition.Evaluator.Target)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_experiments
		SET name = $3, candidate_signature = $4, repetitions = $5,
		    per_cell_budget_usd = $6, total_budget_usd = $7, max_tokens_per_cell = $8,
		    evaluator_target_kind = $9, evaluator_workflow_name = $10,
		    evaluator_definition_id = $11, evaluator_workflow_version = $12,
		    evaluator_function_id = $13, evaluator_node_parameters = $14,
		    evaluator_signature = $15,
		    evaluator_measurements_port = $16, revision = revision + 1, updated_at = now()
		WHERE id = $1 AND team_id = $2 AND state = 'draft' AND revision = $17
	`, int64(id), teamID, definition.Name, candidateSignature, definition.Repetitions,
		definition.Budget.PerCellUSD, definition.Budget.TotalUSD, definition.Budget.MaxTokensPerCell,
		string(definition.Evaluator.Target.Kind), definition.Evaluator.Target.WorkflowName,
		definition.Evaluator.Target.DefinitionID, definition.Evaluator.Target.Version,
		nullableString(definition.Evaluator.Target.FunctionID), evaluatorNodeParameters,
		evaluatorSignature, definition.Evaluator.MeasurementsPort, revision)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if affected != 1 {
		return experiment.StoredExperiment{}, experiment.ErrRevisionConflict
	}
	if err := insertExperimentDefinition(ctx, tx, id, teamID, definition); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := appendExperimentAuditEvent(
		ctx,
		tx,
		teamID,
		id,
		experiment.AuditUpdate,
		actor,
		revision+1,
	); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := tx.Commit(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	stored, found, err := factory.Get(ctx, teamID, id)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if !found {
		return experiment.StoredExperiment{}, experiment.ErrNotFound
	}
	return stored, nil
}

func (factory *agentExperimentsFactory) Get(
	ctx context.Context,
	teamID int,
	id experiment.ID,
) (experiment.StoredExperiment, bool, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil {
		return experiment.StoredExperiment{}, false, nil
	}
	stored, err := loadStoredExperiment(ctx, factory.conn, teamID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return experiment.StoredExperiment{}, false, nil
	}
	return stored, err == nil, err
}

func (factory *agentExperimentsFactory) List(ctx context.Context, teamID int) ([]experiment.StoredExperiment, error) {
	return factory.ListPage(ctx, teamID, experiment.ListFilter{Limit: experiment.MaxListedExperiments})
}

func (factory *agentExperimentsFactory) ListPage(
	ctx context.Context,
	teamID int,
	filter experiment.ListFilter,
) ([]experiment.StoredExperiment, error) {
	if ctx == nil || teamID <= 0 {
		return nil, experiment.ErrNotFound
	}
	if filter.Limit <= 0 || filter.Limit > experiment.MaxListedExperiments+1 {
		return nil, fmt.Errorf("db: experiment page limit must be between 1 and %d", experiment.MaxListedExperiments+1)
	}
	if filter.Before != nil {
		if err := filter.Before.Validate(); err != nil {
			return nil, fmt.Errorf("db: invalid experiment page cursor: %w", err)
		}
	}
	query := `
		SELECT id FROM agent_experiments
		WHERE team_id = $1`
	arguments := []any{teamID}
	if filter.Before != nil {
		query += ` AND (created_at, id) < ($2, $3)`
		arguments = append(arguments, filter.Before.CreatedAt, filter.Before.ID)
	}
	query += fmt.Sprintf(`
		ORDER BY created_at DESC, id DESC
		LIMIT $%d
	`, len(arguments)+1)
	arguments = append(arguments, filter.Limit)
	rows, err := factory.conn.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []experiment.ID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, experiment.ID(id))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]experiment.StoredExperiment, 0, len(ids))
	for _, id := range ids {
		value, err := loadStoredExperiment(ctx, factory.conn, teamID, id)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func (factory *agentExperimentsFactory) Start(
	ctx context.Context,
	teamID int,
	id experiment.ID,
	revision int64,
	actor string,
) (experiment.StoredExperiment, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil || revision <= 0 || !validExperimentActor(actor) {
		return experiment.StoredExperiment{}, experiment.ErrInvalidDefinition
	}
	initial, err := factory.PreflightStart(ctx, teamID, id, revision)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	var preparedSources []experiment.PreparedResourceSourceAdmission
	if factory.resourceSourcePreparer != nil {
		preparedSources, err = factory.resourceSourcePreparer.PrepareResourceSources(
			ctx,
			experiment.ResourceSourcePreparation{
				TeamID: teamID, TeamName: initial.TeamName, Actor: actor,
				ExperimentID: id, Definition: initial.Definition,
			},
		)
		if err != nil {
			return experiment.StoredExperiment{}, err
		}
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	defer Rollback(tx)
	preflight, err := factory.preflightStartLocked(ctx, tx, teamID, id, revision)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	stored := preflight.stored
	frozenVariants := preflight.frozenVariants
	frozenEvaluator := preflight.frozenEvaluator
	expectedCells := preflight.expectedCells
	if err := bindExperimentResourceSourceAdmissions(
		ctx, tx, teamID, id, preparedSources,
	); err != nil {
		return experiment.StoredExperiment{}, err
	}
	for index, variant := range stored.Definition.Variants {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_experiment_variants
			SET target_config_hash = $3, dev_validation_provenance_hash = $4
			WHERE experiment_id = $1 AND label = $2 AND target_config_hash IS NULL
			  AND dev_validation_provenance_hash IS NULL
		`, int64(id), variant.Label, frozenVariants[index].targetConfigHash, frozenVariants[index].devValidationProvenanceHash)
		if err != nil {
			return experiment.StoredExperiment{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil || affected != 1 {
			return experiment.StoredExperiment{}, fmt.Errorf("%w: candidate target authority changed during start", experiment.ErrInvalidDefinition)
		}
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_experiment_cells
			(experiment_id, fixture_id, variant_id, repetition, status)
		SELECT e.id, fixture.id, variant.id, repetition.value, 'pending'
		FROM agent_experiments e
		JOIN agent_experiment_fixtures fixture ON fixture.experiment_id = e.id
		JOIN LATERAL (
			SELECT value.*,
			       row_number() OVER (ORDER BY value.id) - 1 AS ordinal,
			       count(*) OVER () AS variant_count
			FROM agent_experiment_variants value
			WHERE value.experiment_id = e.id
		) variant ON true
		CROSS JOIN LATERAL generate_series(1, e.repetitions) AS repetition(value)
		WHERE e.id = $1 AND e.team_id = $2 AND e.state = 'draft' AND e.revision = $3
		ORDER BY repetition.value, fixture.id,
		         mod(variant.ordinal + repetition.value - 1, variant.variant_count),
		         variant.id
	`, int64(id), teamID, revision)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	allocated, err := result.RowsAffected()
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if allocated != int64(expectedCells) {
		return experiment.StoredExperiment{}, fmt.Errorf("%w: incomplete experiment matrix", experiment.ErrInvalidDefinition)
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE agent_experiments
		SET state = 'running', evaluator_target_config_hash = $4,
		    evaluator_dev_validation_provenance_hash = $5,
		    started_at = now(), updated_at = now(), revision = revision + 1
		WHERE id = $1 AND team_id = $2 AND state = 'draft' AND revision = $3
	`, int64(id), teamID, revision, frozenEvaluator.targetConfigHash, frozenEvaluator.devValidationProvenanceHash)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if affected != 1 {
		return experiment.StoredExperiment{}, experiment.ErrRevisionConflict
	}
	if err := appendExperimentAuditEvent(
		ctx,
		tx,
		teamID,
		id,
		experiment.AuditStart,
		actor,
		revision+1,
	); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := tx.Commit(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	value, found, err := factory.Get(ctx, teamID, id)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if !found {
		return experiment.StoredExperiment{}, experiment.ErrNotFound
	}
	return value, nil
}

// PreflightStart runs the exact same authoritative target rendering, budget
// proof, and retained-fixture checks as Start while leaving the draft and its
// frozen target columns untouched.
func (factory *agentExperimentsFactory) PreflightStart(
	ctx context.Context,
	teamID int,
	id experiment.ID,
	revision int64,
) (experiment.StoredExperiment, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil || revision <= 0 {
		return experiment.StoredExperiment{}, experiment.ErrInvalidDefinition
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	defer Rollback(tx)
	preflight, err := factory.preflightStartLocked(ctx, tx, teamID, id, revision)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := tx.Commit(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	return preflight.stored, nil
}

type experimentStartPreflight struct {
	stored          experiment.StoredExperiment
	frozenVariants  []frozenExperimentTarget
	frozenEvaluator frozenExperimentTarget
	expectedCells   int
}

type frozenExperimentTarget struct {
	targetConfigHash            string
	devValidationProvenanceHash string
}

type experimentResourceSourceIdentity struct {
	definitionID int64
	configHash   string
}

// bindExperimentResourceSourceAdmissions requires Start-time preparation to
// exactly cover the source-pipeline identities currently registered for this
// experiment's immutable definitions. This runs after the locked preflight
// and before any child cells or live state are persisted, so a preparation
// race cannot leave a partially started experiment.
func bindExperimentResourceSourceAdmissions(
	ctx context.Context,
	tx Tx,
	teamID int,
	id experiment.ID,
	prepared []experiment.PreparedResourceSourceAdmission,
) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT registered.workflow_definition_id, registered.config_hash
		FROM agent_workflow_resource_source_pipelines registered
		WHERE registered.team_id = $2
		  AND registered.workflow_definition_id IN (
		    SELECT variant.definition_id
		    FROM agent_experiment_variants variant
		    WHERE variant.experiment_id = $1
		    UNION
		    SELECT parent.evaluator_definition_id
		    FROM agent_experiments parent
		    WHERE parent.id = $1 AND parent.team_id = $2
		  )
		ORDER BY registered.workflow_definition_id, registered.config_hash
	`, int64(id), teamID)
	if err != nil {
		return err
	}
	expected := make([]experimentResourceSourceIdentity, 0)
	for rows.Next() {
		var identity experimentResourceSourceIdentity
		if err := rows.Scan(&identity.definitionID, &identity.configHash); err != nil {
			_ = rows.Close()
			return err
		}
		expected = append(expected, identity)
	}
	if err := rows.Close(); err != nil {
		return err
	}

	provided := make(map[experimentResourceSourceIdentity]int64, len(prepared))
	for _, value := range prepared {
		identity := experimentResourceSourceIdentity{
			definitionID: value.WorkflowDefinitionID,
			configHash:   value.SourceConfigHash,
		}
		if identity.definitionID <= 0 ||
			!lowerHex64.MatchString(identity.configHash) ||
			value.AdmissionID <= 0 {
			return fmt.Errorf("%w: prepared experiment resource source identity is invalid", experiment.ErrInvalidDefinition)
		}
		if _, duplicate := provided[identity]; duplicate {
			return fmt.Errorf("%w: duplicate prepared experiment resource source identity", experiment.ErrInvalidDefinition)
		}
		provided[identity] = value.AdmissionID
	}
	if len(provided) != len(expected) {
		return fmt.Errorf("%w: experiment resource source preparations do not cover its immutable targets", experiment.ErrInvalidDefinition)
	}
	for _, identity := range expected {
		admissionID, found := provided[identity]
		if !found {
			return fmt.Errorf("%w: experiment resource source definition or hash changed during start", experiment.ErrInvalidDefinition)
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO agent_experiment_resource_source_admissions
				(experiment_id, team_id, workflow_definition_id,
				 source_config_hash, resource_source_admission_id)
			SELECT parent.id, parent.team_id, admission.workflow_definition_id,
			       admission.source_config_hash, admission.id
			FROM agent_experiments parent
			JOIN agent_workflow_resource_source_admissions admission
			  ON admission.id = $5
			 AND admission.team_id = parent.team_id
			 AND admission.workflow_definition_id = $3
			 AND admission.source_config_hash = $4
			 AND admission.status = 'ready'
			WHERE parent.id = $1 AND parent.team_id = $2
			  AND parent.state = 'draft'
			ON CONFLICT (experiment_id, workflow_definition_id, source_config_hash)
			DO UPDATE SET resource_source_admission_id =
				agent_experiment_resource_source_admissions.resource_source_admission_id
			WHERE agent_experiment_resource_source_admissions.resource_source_admission_id =
				EXCLUDED.resource_source_admission_id
		`, int64(id), teamID, identity.definitionID, identity.configHash, admissionID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("%w: prepared experiment resource source admission is unavailable or drifted", experiment.ErrInvalidDefinition)
		}
	}
	return nil
}

func (factory *agentExperimentsFactory) preflightStartLocked(
	ctx context.Context,
	tx Tx,
	teamID int,
	id experiment.ID,
	revision int64,
) (experimentStartPreflight, error) {
	state, currentRevision, err := lockExperiment(ctx, tx, teamID, id)
	if err != nil {
		return experimentStartPreflight{}, err
	}
	if state != experiment.StateDraft {
		return experimentStartPreflight{}, experiment.ErrImmutable
	}
	if currentRevision != revision {
		return experimentStartPreflight{}, experiment.ErrRevisionConflict
	}
	stored, err := loadStoredExperiment(ctx, tx, teamID, id)
	if err != nil {
		return experimentStartPreflight{}, err
	}
	if err := stored.Definition.ValidateStart(); err != nil {
		return experimentStartPreflight{}, fmt.Errorf("%w: %v", experiment.ErrInvalidDefinition, err)
	}
	if err := validateAuthoritativeExperimentTargets(ctx, tx, stored.Definition); err != nil {
		return experimentStartPreflight{}, err
	}
	frozenVariants, frozenEvaluator, err := freezeAuthoritativeExperimentTargets(
		ctx,
		tx,
		stored.Definition,
		factory.targetRenderer,
		factory.budgetConfig.GlobalDailyCapUSD > 0,
	)
	if err != nil {
		return experimentStartPreflight{}, err
	}
	if err := validateStoredExperimentFixturesAvailable(ctx, tx, teamID, id); err != nil {
		return experimentStartPreflight{}, err
	}
	expectedCells, err := stored.Definition.MaterializedCellCount()
	if err != nil {
		return experimentStartPreflight{}, fmt.Errorf("%w: %v", experiment.ErrInvalidDefinition, err)
	}
	return experimentStartPreflight{
		stored: stored, frozenVariants: frozenVariants,
		frozenEvaluator: frozenEvaluator, expectedCells: expectedCells,
	}, nil
}

func (factory *agentExperimentsFactory) Cancel(
	ctx context.Context,
	teamID int,
	id experiment.ID,
	revision int64,
	actor string,
) (experiment.StoredExperiment, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil || revision <= 0 || !validExperimentActor(actor) {
		return experiment.StoredExperiment{}, experiment.ErrInvalidDefinition
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	defer Rollback(tx)
	state, currentRevision, err := lockExperiment(ctx, tx, teamID, id)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if currentRevision != revision {
		return experiment.StoredExperiment{}, experiment.ErrRevisionConflict
	}
	if state == experiment.StateCanceling {
		if err := tx.Commit(); err != nil {
			return experiment.StoredExperiment{}, err
		}
		value, found, err := factory.Get(ctx, teamID, id)
		if err != nil {
			return experiment.StoredExperiment{}, err
		}
		if !found {
			return experiment.StoredExperiment{}, experiment.ErrNotFound
		}
		return value, nil
	}
	if state != experiment.StateRunning {
		return experiment.StoredExperiment{}, experiment.ErrImmutable
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_experiments
		SET state = 'canceling', updated_at = now(), revision = revision + 1
		WHERE id = $1 AND team_id = $2 AND revision = $3
		  AND state = 'running'
	`, int64(id), teamID, revision)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if affected != 1 {
		return experiment.StoredExperiment{}, experiment.ErrRevisionConflict
	}
	if err := appendExperimentAuditEvent(
		ctx,
		tx,
		teamID,
		id,
		experiment.AuditCancel,
		actor,
		revision+1,
	); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if err := tx.Commit(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	value, found, err := factory.Get(ctx, teamID, id)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	if !found {
		return experiment.StoredExperiment{}, experiment.ErrNotFound
	}
	return value, nil
}

func appendExperimentAuditEvent(
	ctx context.Context,
	tx Tx,
	teamID int,
	id experiment.ID,
	action experiment.AuditAction,
	actor string,
	revision int64,
) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_experiment_audit_events
			(experiment_id, team_id, team_name, action, actor, revision)
		SELECT id, team_id, team_name, $3, $4, $5
		FROM agent_experiments
		WHERE id = $1 AND team_id = $2
	`, int64(id), teamID, string(action), actor, revision)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return experiment.ErrNotFound
	}
	return nil
}

func (factory *agentExperimentsFactory) ClaimCancellations(
	ctx context.Context,
	limit int,
) ([]experiment.CancellationWork, error) {
	if ctx == nil || limit <= 0 {
		return nil, fmt.Errorf("db: experiment cancellation claim limit must be positive")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT id, team_id
		FROM agent_experiments
		WHERE state = 'canceling'
		ORDER BY updated_at, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var work []experiment.CancellationWork
	for rows.Next() {
		var item experiment.CancellationWork
		if err := rows.Scan(&item.ExperimentID, &item.TeamID); err != nil {
			return nil, err
		}
		work = append(work, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range work {
		runRows, err := factory.conn.QueryContext(ctx, `
			SELECT run_id
			FROM (
				SELECT cell.candidate_workflow_run_id AS run_id
				FROM agent_experiment_cells cell
				WHERE cell.experiment_id = $1 AND cell.candidate_workflow_run_id IS NOT NULL
				UNION
				SELECT evaluation.evaluator_workflow_run_id
				FROM agent_experiment_cells cell
				JOIN agent_experiment_evaluations evaluation ON evaluation.cell_id = cell.id
				WHERE cell.experiment_id = $1 AND evaluation.evaluator_workflow_run_id IS NOT NULL
				UNION
				SELECT run.id
				FROM agent_experiment_cells cell
				JOIN agent_workflow_runs run
				  ON run.team_id = $2 AND run.origin_kind = 'experiment'
				 AND run.origin_reference IN (
					'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text,
					'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text || ':evaluator'
				 )
				WHERE cell.experiment_id = $1
			) linked
			WHERE run_id IS NOT NULL
			ORDER BY run_id
		`, int64(work[index].ExperimentID), work[index].TeamID)
		if err != nil {
			return nil, err
		}
		for runRows.Next() {
			var runID snapshot.WorkflowRunID
			if err := runRows.Scan(&runID); err != nil {
				_ = runRows.Close()
				return nil, err
			}
			work[index].WorkflowRunIDs = append(work[index].WorkflowRunIDs, runID)
		}
		if err := runRows.Close(); err != nil {
			return nil, err
		}
	}
	return work, nil
}

func (factory *agentExperimentsFactory) FinalizeCancellation(
	ctx context.Context,
	teamID int,
	id experiment.ID,
) (bool, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil {
		return false, nil
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return false, err
	}
	var state experiment.State
	err = tx.QueryRowContext(ctx, `
		SELECT state FROM agent_experiments
		WHERE id = $1 AND team_id = $2 FOR UPDATE
	`, int64(id), teamID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if state != experiment.StateCanceling {
		return false, nil
	}
	var ready bool
	err = tx.QueryRowContext(ctx, `
		SELECT NOT EXISTS (
			SELECT 1
			FROM agent_workflow_runs run
			WHERE run.team_id = $2 AND run.status IN ('admitting', 'running', 'canceling')
			  AND (
				run.id IN (
					SELECT cell.candidate_workflow_run_id
					FROM agent_experiment_cells cell
					WHERE cell.experiment_id = $1 AND cell.candidate_workflow_run_id IS NOT NULL
					UNION
					SELECT evaluation.evaluator_workflow_run_id
					FROM agent_experiment_cells cell
					JOIN agent_experiment_evaluations evaluation ON evaluation.cell_id = cell.id
					WHERE cell.experiment_id = $1 AND evaluation.evaluator_workflow_run_id IS NOT NULL
				)
				OR EXISTS (
					SELECT 1 FROM agent_experiment_cells cell
					WHERE cell.experiment_id = $1
					  AND run.origin_kind = 'experiment'
					  AND run.origin_reference IN (
						'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text,
						'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text || ':evaluator'
					  )
				)
			  )
		)
	`, int64(id), teamID).Scan(&ready)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_experiment_evaluations evaluation
		SET status = 'canceled', completed_at = now(), updated_at = now()
		FROM agent_experiment_cells cell
		WHERE evaluation.cell_id = cell.id AND cell.experiment_id = $1
		  AND evaluation.status IS NULL
	`, int64(id)); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_experiment_cells
		SET status = 'canceled', completed_at = now(), updated_at = now(), lease_until = NULL
		WHERE experiment_id = $1 AND status IN ('pending', 'running')
	`, int64(id)); err != nil {
		return false, err
	}
	reservationRows, err := tx.QueryContext(ctx, `
		SELECT reservation.cell_id
		FROM agent_experiment_budget_reservations reservation
		WHERE reservation.experiment_id = $1 AND reservation.state = 'active'
		ORDER BY reservation.cell_id
	`, int64(id))
	if err != nil {
		return false, err
	}
	var reservedCells []experiment.CellID
	for reservationRows.Next() {
		var cellID experiment.CellID
		if err := reservationRows.Scan(&cellID); err != nil {
			_ = reservationRows.Close()
			return false, err
		}
		reservedCells = append(reservedCells, cellID)
	}
	if err := reservationRows.Close(); err != nil {
		return false, err
	}
	for _, cellID := range reservedCells {
		if err := settleExperimentBudgetReservation(ctx, tx, cellID); err != nil {
			return false, err
		}
	}
	frozenScorecard, err := marshalExperimentScorecard(ctx, tx, teamID, id)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_experiments
		SET state = 'canceled', completed_at = now(), updated_at = now(),
		    revision = revision + 1, frozen_scorecard = $3
		WHERE id = $1 AND team_id = $2 AND state = 'canceling'
		  AND frozen_scorecard IS NULL
	`, int64(id), teamID, frozenScorecard)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, fmt.Errorf("db: failed to atomically cancel and freeze experiment scorecard")
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (factory *agentExperimentsFactory) ListCells(
	ctx context.Context,
	teamID int,
	id experiment.ID,
) ([]experiment.StoredCell, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil {
		return nil, experiment.ErrNotFound
	}
	var exists bool
	if err := factory.conn.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM agent_experiments WHERE id = $1 AND team_id = $2)
	`, int64(id), teamID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, experiment.ErrNotFound
	}
	rows, err := factory.conn.QueryContext(ctx, storedCellsQuery+`
		WHERE cell.experiment_id = $1 AND experiment.team_id = $2
		ORDER BY cell.id
		LIMIT $3
	`, int64(id), teamID, experiment.MaxMaterializedCells+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]experiment.StoredCell, 0)
	for rows.Next() {
		value, err := scanStoredExperimentCell(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
		if len(values) > experiment.MaxMaterializedCells {
			return nil, fmt.Errorf("db: experiment cell count exceeds admitted limit of %d", experiment.MaxMaterializedCells)
		}
	}
	return values, rows.Err()
}

func (factory *agentExperimentsFactory) GetCell(
	ctx context.Context,
	teamID int,
	id experiment.ID,
	cellID experiment.CellID,
) (experiment.StoredCell, bool, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil || cellID.Validate() != nil {
		return experiment.StoredCell{}, false, nil
	}
	value, err := scanStoredExperimentCell(factory.conn.QueryRowContext(ctx, storedCellsQuery+`
		WHERE cell.id = $1 AND cell.experiment_id = $2 AND experiment.team_id = $3
	`, int64(cellID), int64(id), teamID))
	if errors.Is(err, sql.ErrNoRows) {
		return experiment.StoredCell{}, false, nil
	}
	return value, err == nil, err
}

func (factory *agentExperimentsFactory) ClaimCandidateCells(
	ctx context.Context,
	limit int,
) ([]experiment.CandidateCell, error) {
	if ctx == nil || limit <= 0 {
		return nil, fmt.Errorf("db: experiment candidate claim limit must be positive")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	rows, err := tx.QueryContext(ctx, `
		WITH selected AS (
			SELECT cell.id
			FROM agent_experiment_cells cell
			JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
			WHERE experiment.state = 'running'
			  AND cell.status IN ('pending', 'running')
			  AND cell.candidate_workflow_run_id IS NULL
			  AND (cell.lease_until IS NULL OR cell.lease_until <= now())
			ORDER BY cell.id
			FOR UPDATE OF cell SKIP LOCKED
			LIMIT $1
		)
		UPDATE agent_experiment_cells cell
		SET status = 'running', lease_until = now() + make_interval(secs => $2), updated_at = now()
		FROM selected
		WHERE cell.id = selected.id
		RETURNING cell.id
	`, limit, agentExperimentLeaseSeconds)
	if err != nil {
		return nil, err
	}
	ids, err := scanExperimentCellIDs(rows)
	if err != nil {
		return nil, err
	}
	values := make([]experiment.CandidateCell, 0, len(ids))
	for _, id := range ids {
		value, err := loadCandidateCell(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

func (factory *agentExperimentsFactory) RecordCandidateRun(
	ctx context.Context,
	cellID experiment.CellID,
	runID snapshot.WorkflowRunID,
) (bool, error) {
	if ctx == nil || cellID.Validate() != nil || runID.Validate() != nil {
		return false, nil
	}
	return recordCandidateRun(ctx, factory.conn, cellID, runID)
}

// CreateAndRecordCandidateRun deliberately invokes create without a database
// transaction. The workflow-run allocator carries the experiment admission
// gate and serializes its short allocation transaction with Cancel. Once a
// durable child exists, origin-based cancellation can discover it even before
// this exact association is recorded.
func (factory *agentExperimentsFactory) CreateAndRecordCandidateRun(
	ctx context.Context,
	cell experiment.CandidateCell,
	create experiment.CandidateRunCreator,
) (experiment.CandidateRunAdmission, error) {
	if ctx == nil || cell.Validate() != nil || create == nil {
		return experiment.CandidateRunAdmission{}, fmt.Errorf("db: invalid candidate admission")
	}
	result, bindErr := create(ctx)
	if errors.Is(bindErr, experiment.ErrBindAdmissionClosed) {
		return experiment.CandidateRunAdmission{}, nil
	}
	admission := experiment.CandidateRunAdmission{
		Started: true, Result: result, BindError: bindErr,
	}
	if bindErr == nil && result.WorkflowRunID.Validate() == nil &&
		result.WorkflowDefinitionID == cell.Target.DefinitionID &&
		result.WorkflowName == cell.Target.WorkflowName &&
		result.WorkflowVersion == cell.Target.Version &&
		result.FunctionID == cell.Target.FunctionID &&
		result.TargetConfigHash == cell.TargetConfigHash &&
		result.DevValidationProvenanceHash == cell.DevValidationProvenanceHash {
		var err error
		admission.Recorded, err = recordCandidateRun(ctx, factory.conn, cell.ID, result.WorkflowRunID)
		if err != nil {
			return experiment.CandidateRunAdmission{}, err
		}
	}
	return admission, nil
}

func recordCandidateRun(
	ctx context.Context,
	queryer agentExperimentQueryer,
	cellID experiment.CellID,
	runID snapshot.WorkflowRunID,
) (bool, error) {
	var recorded int64
	err := queryer.QueryRowContext(ctx, `
		UPDATE agent_experiment_cells cell
		SET candidate_workflow_run_id = $2, lease_until = NULL,
		    status = CASE WHEN experiment.state = 'running' THEN 'running' ELSE cell.status END,
		    updated_at = now()
		FROM agent_experiments experiment,
		     agent_experiment_variants variant,
		     agent_workflow_runs linked_run,
		     LATERAL (
				SELECT count(*)::integer AS association_count,
				       min(resource_source_admission_id) AS resource_source_admission_id
				FROM agent_experiment_resource_source_admissions source
				WHERE source.experiment_id = experiment.id
				  AND source.team_id = experiment.team_id
				  AND source.workflow_definition_id = variant.definition_id
		     ) source
		WHERE cell.id = $1 AND experiment.id = cell.experiment_id
		  AND variant.id = cell.variant_id AND linked_run.id = $2
		  AND experiment.state IN ('running', 'canceling', 'canceled')
		  AND cell.status IN ('pending', 'running', 'canceled')
		  AND linked_run.team_id = experiment.team_id
		  -- The runner's bindResultMatchesTarget verifies the executable kind
		  -- because none of the other compared coordinates is guaranteed to
		  -- differ between a node run and a same-named workflow run. This
		  -- durable association must verify it too, or the database would
		  -- adopt the wrong run before the runner ever inspected the result.
		  -- Absent is read as 'workflow', matching that helper.
		  AND COALESCE(NULLIF(btrim(linked_run.definition_kind), ''), 'workflow') =
		      CASE WHEN variant.target_kind = 'node' THEN 'node' ELSE 'workflow' END
		  AND linked_run.workflow_definition_id = variant.definition_id
		  AND linked_run.workflow_name = variant.workflow_name
		  AND linked_run.workflow_version = variant.workflow_version
		  AND linked_run.function_id IS NOT DISTINCT FROM variant.function_id
		  AND linked_run.parameterized_config_hash = variant.target_config_hash
		  AND linked_run.dev_validation_provenance_hash = variant.dev_validation_provenance_hash
		  AND linked_run.origin_kind = 'experiment'
		  AND linked_run.origin_reference =
		      'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text
		  AND source.association_count <= 1
		  AND linked_run.resource_source_admission_id IS NOT DISTINCT FROM
		      source.resource_source_admission_id
		  AND (cell.candidate_workflow_run_id IS NULL OR cell.candidate_workflow_run_id = $2)
		RETURNING cell.candidate_workflow_run_id
	`, int64(cellID), int64(runID)).Scan(&recorded)
	if err == nil {
		return recorded == int64(runID), nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	var existing sql.NullInt64
	err = queryer.QueryRowContext(ctx, `
		SELECT cell.candidate_workflow_run_id
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		JOIN agent_experiment_variants variant ON variant.id = cell.variant_id
		JOIN agent_workflow_runs linked_run ON linked_run.id = $2
		CROSS JOIN LATERAL (
			SELECT count(*)::integer AS association_count,
			       min(resource_source_admission_id) AS resource_source_admission_id
			FROM agent_experiment_resource_source_admissions source
			WHERE source.experiment_id = cell.experiment_id
			  AND source.team_id = experiment.team_id
			  AND source.workflow_definition_id = variant.definition_id
		) source
		WHERE cell.id = $1
		  AND source.association_count <= 1
		  AND linked_run.resource_source_admission_id IS NOT DISTINCT FROM
		      source.resource_source_admission_id
	`, int64(cellID), int64(runID)).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return existing.Valid && existing.Int64 == int64(runID), err
}

func (factory *agentExperimentsFactory) RecordCandidateFailure(
	ctx context.Context,
	cellID experiment.CellID,
	category string,
) error {
	if ctx == nil || cellID.Validate() != nil {
		return fmt.Errorf("db: invalid experiment candidate failure")
	}
	status := experiment.CellCandidatePlatformFailure
	switch category {
	case "invalid_admission":
		status = experiment.CellCandidateContractFailure
	case "budget_denied":
		status = experiment.CellSkippedBudget
	case "platform_failure":
	default:
		return fmt.Errorf("db: invalid experiment candidate failure category %q", category)
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_experiment_cells cell
		SET status = $3, candidate_failure_category = $2, lease_until = NULL,
		    completed_at = now(), updated_at = now()
		FROM agent_experiments experiment
		WHERE cell.id = $1 AND experiment.id = cell.experiment_id
		  AND experiment.state = 'running' AND cell.status IN ('pending', 'running')
		  AND cell.candidate_workflow_run_id IS NULL
	`, int64(cellID), category, string(status))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return tx.Commit()
	}
	if err := settleExperimentBudgetReservation(ctx, tx, cellID); err != nil {
		return err
	}
	if err := completeExperimentIfTerminalTx(ctx, tx, cellID); err != nil {
		return err
	}
	return tx.Commit()
}

func (factory *agentExperimentsFactory) ClaimEvaluationCells(
	ctx context.Context,
	limit int,
) ([]experiment.EvaluationCell, error) {
	if ctx == nil || limit <= 0 {
		return nil, fmt.Errorf("db: experiment evaluation claim limit must be positive")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer Rollback(tx)
	rows, err := tx.QueryContext(ctx, `
		WITH selected AS (
			SELECT cell.id
			FROM agent_experiment_cells cell
			JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
			WHERE experiment.state = 'running' AND cell.status = 'running'
			  AND cell.candidate_workflow_run_id IS NOT NULL
			  AND (cell.lease_until IS NULL OR cell.lease_until <= now())
			ORDER BY cell.id
			FOR UPDATE OF cell SKIP LOCKED
			LIMIT $1
		)
		UPDATE agent_experiment_cells cell
		SET lease_until = now() + make_interval(secs => $2), updated_at = now()
		FROM selected
		WHERE cell.id = selected.id
		RETURNING cell.id
	`, limit, agentExperimentLeaseSeconds)
	if err != nil {
		return nil, err
	}
	ids, err := scanExperimentCellIDs(rows)
	if err != nil {
		return nil, err
	}
	values := make([]experiment.EvaluationCell, 0, len(ids))
	for _, id := range ids {
		value, err := loadEvaluationCell(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

func (factory *agentExperimentsFactory) RecordEvaluatorRun(
	ctx context.Context,
	cellID experiment.CellID,
	runID snapshot.WorkflowRunID,
) (bool, error) {
	if ctx == nil || cellID.Validate() != nil || runID.Validate() != nil {
		return false, nil
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	recorded, err := recordEvaluatorRun(ctx, tx, cellID, runID)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return recorded, nil
}

func (factory *agentExperimentsFactory) CreateAndRecordEvaluatorRun(
	ctx context.Context,
	cell experiment.EvaluationCell,
	create experiment.EvaluatorRunCreator,
) (experiment.EvaluatorRunAdmission, error) {
	if ctx == nil || cell.Validate() != nil || create == nil {
		return experiment.EvaluatorRunAdmission{}, fmt.Errorf("db: invalid evaluator admission")
	}
	result, bindErr := create(ctx)
	if errors.Is(bindErr, experiment.ErrBindAdmissionClosed) {
		return experiment.EvaluatorRunAdmission{}, nil
	}
	admission := experiment.EvaluatorRunAdmission{
		Started: true, Result: result, BindError: bindErr,
	}
	if bindErr == nil && result.WorkflowRunID.Validate() == nil &&
		result.WorkflowDefinitionID == cell.Evaluator.Target.DefinitionID &&
		result.WorkflowName == cell.Evaluator.Target.WorkflowName &&
		result.WorkflowVersion == cell.Evaluator.Target.Version &&
		result.FunctionID == cell.Evaluator.Target.FunctionID &&
		result.TargetConfigHash == cell.Evaluator.TargetConfigHash &&
		result.DevValidationProvenanceHash == cell.Evaluator.DevValidationProvenanceHash {
		var err error
		admission.Recorded, err = factory.RecordEvaluatorRun(ctx, cell.ID, result.WorkflowRunID)
		if err != nil {
			return experiment.EvaluatorRunAdmission{}, err
		}
	}
	return admission, nil
}

func recordEvaluatorRun(
	ctx context.Context,
	tx Tx,
	cellID experiment.CellID,
	runID snapshot.WorkflowRunID,
) (bool, error) {
	var recorded int64
	err := tx.QueryRowContext(ctx, `
		INSERT INTO agent_experiment_evaluations
			(cell_id, evaluator_workflow_run_id, status, completed_at)
		SELECT cell.id, $2,
		       CASE WHEN experiment.state = 'running' THEN NULL ELSE 'canceled' END,
		       CASE WHEN experiment.state = 'running' THEN NULL ELSE now() END
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		JOIN agent_workflow_runs linked_run ON linked_run.id = $2
		CROSS JOIN LATERAL (
			SELECT count(*)::integer AS association_count,
			       min(resource_source_admission_id) AS resource_source_admission_id
			FROM agent_experiment_resource_source_admissions source
			WHERE source.experiment_id = cell.experiment_id
			  AND source.team_id = experiment.team_id
			  AND source.workflow_definition_id = experiment.evaluator_definition_id
		) source
		WHERE cell.id = $1 AND cell.status IN ('running', 'canceled')
		  AND experiment.state IN ('running', 'canceling', 'canceled')
		  AND linked_run.team_id = experiment.team_id
		  -- Same rule as the candidate association: the evaluator target
		  -- carries a kind the runner checks, so the durable association has
		  -- to agree before it adopts a run. Absent is read as 'workflow'.
		  AND COALESCE(NULLIF(btrim(linked_run.definition_kind), ''), 'workflow') =
		      CASE WHEN experiment.evaluator_target_kind = 'node' THEN 'node' ELSE 'workflow' END
		  AND linked_run.workflow_definition_id = experiment.evaluator_definition_id
		  AND linked_run.workflow_name = experiment.evaluator_workflow_name
		  AND linked_run.workflow_version = experiment.evaluator_workflow_version
		  AND linked_run.function_id IS NOT DISTINCT FROM experiment.evaluator_function_id
		  AND linked_run.parameterized_config_hash = experiment.evaluator_target_config_hash
		  AND linked_run.dev_validation_provenance_hash = experiment.evaluator_dev_validation_provenance_hash
		  AND linked_run.origin_kind = 'experiment'
		  AND linked_run.origin_reference =
		      'experiment:' || cell.experiment_id::text || ':cell:' || cell.id::text || ':evaluator'
		  AND source.association_count <= 1
		  AND linked_run.resource_source_admission_id IS NOT DISTINCT FROM
		      source.resource_source_admission_id
		ON CONFLICT (cell_id) DO UPDATE
		SET evaluator_workflow_run_id = EXCLUDED.evaluator_workflow_run_id,
		    status = COALESCE(agent_experiment_evaluations.status, EXCLUDED.status),
		    completed_at = COALESCE(agent_experiment_evaluations.completed_at, EXCLUDED.completed_at),
		    updated_at = now()
		WHERE agent_experiment_evaluations.evaluator_workflow_run_id IS NULL
		   OR agent_experiment_evaluations.evaluator_workflow_run_id = EXCLUDED.evaluator_workflow_run_id
		RETURNING evaluator_workflow_run_id
	`, int64(cellID), int64(runID)).Scan(&recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_experiment_cells SET lease_until = NULL, updated_at = now() WHERE id = $1
	`, int64(cellID)); err != nil {
		return false, err
	}
	return recorded == int64(runID), nil
}

func (factory *agentExperimentsFactory) RecordMeasurements(
	ctx context.Context,
	cellID experiment.CellID,
	document contracts.MeasurementsDocument,
) error {
	if ctx == nil || cellID.Validate() != nil {
		return fmt.Errorf("db: invalid experiment measurements")
	}
	if len(document.Metrics) > experiment.MaxMeasurementsPerCell {
		return fmt.Errorf("db: experiment measurements exceed limit of %d", experiment.MaxMeasurementsPerCell)
	}
	if err := document.ValidateDetached(); err != nil ||
		(document.Conclusion != "measured" && document.Conclusion != "partial") {
		return fmt.Errorf("db: invalid experiment measurements: %v", err)
	}
	payload, err := json.Marshal(document)
	if err != nil {
		return err
	}
	result, err := factory.conn.ExecContext(ctx, `
		INSERT INTO agent_experiment_evaluations (cell_id, measurements)
		SELECT cell.id, $2
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		WHERE cell.id = $1 AND cell.status = 'running' AND experiment.state = 'running'
		ON CONFLICT (cell_id) DO UPDATE
		SET measurements = EXCLUDED.measurements, updated_at = now()
		WHERE agent_experiment_evaluations.measurements IS NULL
		   OR agent_experiment_evaluations.measurements = EXCLUDED.measurements
	`, int64(cellID), payload)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("db: experiment measurements conflict with an immutable result")
	}
	return nil
}

func (factory *agentExperimentsFactory) CompleteEvaluation(
	ctx context.Context,
	cellID experiment.CellID,
	status experiment.CellStatus,
	measurementID *snapshot.SnapshotID,
) (bool, error) {
	if ctx == nil || cellID.Validate() != nil || !terminalExperimentCellStatus(status) {
		return false, nil
	}
	if measurementID != nil && measurementID.Validate() != nil {
		return false, nil
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	if err := lockAgentBudgetAccounting(ctx, tx); err != nil {
		return false, err
	}
	var currentStatus experiment.CellStatus
	err = tx.QueryRowContext(ctx, `
		SELECT status FROM agent_experiment_cells WHERE id = $1 FOR UPDATE
	`, int64(cellID)).Scan(&currentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if terminalExperimentCellStatus(currentStatus) {
		var storedStatus sql.NullString
		var storedMeasurement sql.NullInt64
		err := tx.QueryRowContext(ctx, `
			SELECT status, measurement_snapshot_id
			FROM agent_experiment_evaluations WHERE cell_id = $1
		`, int64(cellID)).Scan(&storedStatus, &storedMeasurement)
		if errors.Is(err, sql.ErrNoRows) {
			return currentStatus == status && measurementID == nil, nil
		}
		if err != nil {
			return false, err
		}
		return currentStatus == status && storedStatus.String == string(status) &&
			nullableSnapshotIDEqual(storedMeasurement, measurementID), nil
	}
	if currentStatus != experiment.CellRunning && currentStatus != experiment.CellPending {
		return false, nil
	}
	var measurement any
	if measurementID != nil {
		measurement = int64(*measurementID)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_experiment_evaluations
		SET measurement_snapshot_id = $3, status = $2, completed_at = now(), updated_at = now()
		WHERE cell_id = $1 AND status IS NULL
		  AND ($3::bigint IS NULL OR measurements IS NOT NULL)
	`, int64(cellID), string(status), measurement)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 && measurementID == nil {
		result, err = tx.ExecContext(ctx, `
			INSERT INTO agent_experiment_evaluations (cell_id, status, completed_at)
			VALUES ($1, $2, now())
			ON CONFLICT (cell_id) DO NOTHING
		`, int64(cellID), string(status))
		if err != nil {
			return false, err
		}
		affected, err = result.RowsAffected()
		if err != nil {
			return false, err
		}
	}
	if affected != 1 {
		return false, nil
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE agent_experiment_cells
		SET status = $2, completed_at = now(), lease_until = NULL, updated_at = now()
		WHERE id = $1 AND status IN ('pending', 'running')
	`, int64(cellID), string(status))
	if err != nil {
		return false, err
	}
	affected, err = result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected != 1 {
		return false, nil
	}
	if err := settleExperimentBudgetReservation(ctx, tx, cellID); err != nil {
		return false, err
	}
	if err := completeExperimentIfTerminalTx(ctx, tx, cellID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (factory *agentExperimentsFactory) Scorecard(
	ctx context.Context,
	teamID int,
	id experiment.ID,
) (experiment.Scorecard, error) {
	if ctx == nil || teamID <= 0 || id.Validate() != nil {
		return experiment.Scorecard{}, experiment.ErrNotFound
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return experiment.Scorecard{}, err
	}
	defer Rollback(tx)

	var state experiment.State
	var frozenScorecard []byte
	err = tx.QueryRowContext(ctx, `
		SELECT state, frozen_scorecard
		FROM agent_experiments
		WHERE id = $1 AND team_id = $2
	`, int64(id), teamID).Scan(&state, &frozenScorecard)
	if errors.Is(err, sql.ErrNoRows) {
		return experiment.Scorecard{}, experiment.ErrNotFound
	}
	if err != nil {
		return experiment.Scorecard{}, err
	}
	switch state {
	case experiment.StateCompleted, experiment.StateCanceled, experiment.StateFailed:
	default:
		return experiment.Scorecard{}, experiment.ErrScorecardUnavailable
	}

	if len(frozenScorecard) == 0 {
		err = tx.QueryRowContext(ctx, `
			SELECT state, frozen_scorecard
			FROM agent_experiments
			WHERE id = $1 AND team_id = $2
			FOR UPDATE
		`, int64(id), teamID).Scan(&state, &frozenScorecard)
		if errors.Is(err, sql.ErrNoRows) {
			return experiment.Scorecard{}, experiment.ErrNotFound
		}
		if err != nil {
			return experiment.Scorecard{}, err
		}
		switch state {
		case experiment.StateCompleted, experiment.StateCanceled, experiment.StateFailed:
		default:
			return experiment.Scorecard{}, experiment.ErrScorecardUnavailable
		}
	}
	if len(frozenScorecard) == 0 {
		// Experiments terminalized before the frozen-scorecard migration have
		// no payload. Freeze their first post-upgrade view while holding the
		// experiment row lock; all later reads use this exact document.
		frozenScorecard, err = marshalExperimentScorecard(ctx, tx, teamID, id)
		if err != nil {
			return experiment.Scorecard{}, err
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_experiments
			SET frozen_scorecard = $3
			WHERE id = $1 AND team_id = $2 AND frozen_scorecard IS NULL
			  AND state IN ('completed', 'canceled', 'failed')
		`, int64(id), teamID, frozenScorecard)
		if err != nil {
			return experiment.Scorecard{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return experiment.Scorecard{}, err
		}
		if affected != 1 {
			return experiment.Scorecard{}, fmt.Errorf("db: failed to freeze legacy experiment scorecard")
		}
	}

	var value experiment.Scorecard
	if err := json.Unmarshal(frozenScorecard, &value); err != nil {
		return experiment.Scorecard{}, fmt.Errorf("db: decode frozen experiment scorecard: %w", err)
	}
	if value.ExperimentID != id {
		return experiment.Scorecard{}, fmt.Errorf(
			"db: frozen experiment scorecard identity %s does not match %s",
			value.ExperimentID.String(),
			id.String(),
		)
	}
	if err := tx.Commit(); err != nil {
		return experiment.Scorecard{}, err
	}
	return value, nil
}

func marshalExperimentScorecard(
	ctx context.Context,
	queryer agentExperimentQueryer,
	teamID int,
	id experiment.ID,
) ([]byte, error) {
	stored, err := loadStoredExperiment(ctx, queryer, teamID, id)
	if err != nil {
		return nil, err
	}
	value, err := buildExperimentScorecard(ctx, queryer, teamID, id, stored)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("db: encode frozen experiment scorecard: %w", err)
	}
	return payload, nil
}

func buildExperimentScorecard(
	ctx context.Context,
	queryer agentExperimentQueryer,
	teamID int,
	id experiment.ID,
	stored experiment.StoredExperiment,
) (experiment.Scorecard, error) {
	control := ""
	for _, variant := range stored.Definition.Variants {
		if variant.Control {
			control = variant.Label
			break
		}
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT cell.id, variant.label, fixture.label, fixture.role, cell.repetition, cell.status,
		       evaluation.measurements,
		       COALESCE(telemetry.cost_usd, 0), COALESCE(telemetry.wall_time_seconds, 0),
		       COALESCE(telemetry.input_tokens, 0), COALESCE(telemetry.output_tokens, 0),
		       COALESCE(outcomes.interventions, 0)
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		JOIN agent_experiment_variants variant ON variant.id = cell.variant_id
		JOIN agent_experiment_fixtures fixture ON fixture.id = cell.fixture_id
		LEFT JOIN agent_experiment_evaluations evaluation ON evaluation.cell_id = cell.id
		LEFT JOIN agent_workflow_runs candidate_run ON candidate_run.id = cell.candidate_workflow_run_id
		LEFT JOIN agent_workflow_runs evaluator_run ON evaluator_run.id = evaluation.evaluator_workflow_run_id
		LEFT JOIN LATERAL (
			WITH run_ids AS (
				SELECT candidate_run.id WHERE candidate_run.id IS NOT NULL
				UNION
				SELECT evaluator_run.id WHERE evaluator_run.id IS NOT NULL
			), build_ids AS (
				SELECT candidate_run.planned_build_id AS id
				WHERE candidate_run.planned_build_id IS NOT NULL
				UNION
				SELECT evaluator_run.planned_build_id
				WHERE evaluator_run.planned_build_id IS NOT NULL
				UNION
				SELECT anomaly.build_id
				FROM agent_workflow_run_anomalies anomaly
				JOIN run_ids ON run_ids.id = anomaly.workflow_run_id
			)
			SELECT SUM(metric.cost_usd)::double precision AS cost_usd,
			       SUM(metric.wall_time_seconds) AS wall_time_seconds,
			       SUM(metric.input_tokens) AS input_tokens,
			       SUM(metric.output_tokens) AS output_tokens
			FROM agent_run_metrics metric
			WHERE metric.build_id IN (SELECT id FROM build_ids)
		) telemetry ON true
		LEFT JOIN LATERAL (
			SELECT SUM(outcome.intervention_count) AS interventions
			FROM agent_workflow_outcomes outcome
			WHERE outcome.workflow_run_id = cell.candidate_workflow_run_id
		) outcomes ON true
		WHERE experiment.id = $1 AND experiment.team_id = $2
		  AND cell.status NOT IN ('pending', 'running')
		ORDER BY cell.id
		LIMIT $3
	`, int64(id), teamID, experiment.MaxMaterializedCells+1)
	if err != nil {
		return experiment.Scorecard{}, err
	}
	defer rows.Close()
	cellResults := make([]experiment.CellResult, 0)
	controlObserved := false
	for rows.Next() {
		var result experiment.CellResult
		var role string
		var status string
		var rawMeasurements []byte
		var seconds, interventions int64
		if err := rows.Scan(&result.ID, &result.Variant, &result.Fixture, &role, &result.Repetition,
			&status, &rawMeasurements, &result.CostUSD, &seconds, &result.InputTokens,
			&result.OutputTokens, &interventions); err != nil {
			return experiment.Scorecard{}, err
		}
		result.Role = experiment.FixtureRole(role)
		result.Status = experiment.CellStatus(status)
		result.Latency = time.Duration(seconds) * time.Second
		result.HumanInterventions = int(interventions)
		result.NegativeControlPassed = result.Role == experiment.FixtureNegativeControl && result.Status == experiment.CellValidMeasurement
		if len(rawMeasurements) != 0 {
			var document contracts.MeasurementsDocument
			if err := json.Unmarshal(rawMeasurements, &document); err != nil {
				return experiment.Scorecard{}, err
			}
			result.Measurements = document.Metrics
		}
		if result.Variant == control {
			controlObserved = true
		}
		cellResults = append(cellResults, result)
		if len(cellResults) > experiment.MaxMaterializedCells {
			return experiment.Scorecard{}, fmt.Errorf(
				"db: experiment scorecard cell count exceeds admitted limit of %d",
				experiment.MaxMaterializedCells,
			)
		}
	}
	if err := rows.Err(); err != nil {
		return experiment.Scorecard{}, err
	}
	if !controlObserved {
		return experiment.Scorecard{}, experiment.ErrScorecardUnavailable
	}
	expectedCells, countErr := stored.Definition.MaterializedCellCount()
	if countErr != nil {
		return experiment.Scorecard{}, countErr
	}
	expectedCellsPerVariant := expectedCells / len(stored.Definition.Variants)
	value, err := experiment.BuildScorecard(experiment.ScorecardRequest{
		ExperimentID: id, ControlLabel: control,
		ExpectedCellsPerVariant: expectedCellsPerVariant,
		Cells:                   cellResults,
	})
	if err != nil {
		return experiment.Scorecard{}, err
	}
	return value, nil
}

func insertExperimentDefinition(
	ctx context.Context,
	tx Tx,
	experimentID experiment.ID,
	teamID int,
	definition experiment.Definition,
) error {
	for _, variant := range definition.Variants {
		nodeParameters, err := marshalExperimentNodeParameters(variant.Target)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_experiment_variants
				(experiment_id, label, is_control, target_kind, workflow_name,
				 definition_id, workflow_version, function_id, node_parameters, signature_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`, int64(experimentID), variant.Label, variant.Control, string(variant.Target.Kind),
			variant.Target.WorkflowName, variant.Target.DefinitionID, variant.Target.Version,
			nullableString(variant.Target.FunctionID), nodeParameters,
			variant.SignatureHash); err != nil {
			return err
		}
	}
	inputTypes := make(map[string]snapshot.TypeRef, len(definition.Signature.Inputs))
	for _, port := range definition.Signature.Inputs {
		inputTypes[port.Name] = port.Type
	}
	for _, fixture := range definition.Fixtures {
		var fixtureID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO agent_experiment_fixtures (experiment_id, label, role)
			VALUES ($1, $2, $3)
			RETURNING id
		`, int64(experimentID), fixture.Label, string(fixture.Role)).Scan(&fixtureID); err != nil {
			return err
		}
		ports := make([]string, 0, len(fixture.Inputs))
		for port := range fixture.Inputs {
			ports = append(ports, port)
		}
		sort.Strings(ports)
		for _, port := range ports {
			snapshotID := fixture.Inputs[port]
			actor := fmt.Sprintf("experiment:%s:fixture:%d:port:%s", experimentID.String(), fixtureID, port)
			var claimID int64
			err := tx.QueryRowContext(ctx, `
				INSERT INTO agent_snapshot_retention_claims
					(snapshot_id, team_id, class, actor, reason)
				SELECT snapshot.id, snapshot.team_id, 'fixture', $4, 'experiment fixture binding'
				FROM agent_snapshots snapshot
				WHERE snapshot.id = $1 AND snapshot.team_id = $2
				  AND snapshot.type_name || '/v' || snapshot.type_version::text = $3
				  AND snapshot.content_state = 'available'
				RETURNING id
			`, int64(snapshotID), teamID, string(inputTypes[port]), actor).Scan(&claimID)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: fixture %q input %q snapshot is unavailable, unauthorized, or has the wrong type", experiment.ErrInvalidDefinition, fixture.Label, port)
			}
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agent_experiment_fixture_bindings
					(fixture_id, port_name, snapshot_id, retention_claim_id)
				VALUES ($1, $2, $3, $4)
			`, fixtureID, port, int64(snapshotID), claimID); err != nil {
				return err
			}
		}
		for _, assertion := range fixture.Assertions {
			var thresholdTwo any
			if len(assertion.Thresholds) == 2 {
				thresholdTwo = assertion.Thresholds[1]
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO agent_experiment_control_assertions
					(fixture_id, metric_name, comparator, threshold_one, threshold_two)
				VALUES ($1, $2, $3, $4, $5)
			`, fixtureID, assertion.Metric, string(assertion.Comparator), assertion.Thresholds[0], thresholdTwo); err != nil {
				return err
			}
		}
	}
	for _, mapping := range definition.Evaluator.Mappings {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_experiment_evaluator_mappings
				(experiment_id, evaluator_port, source_direction, source_port)
			VALUES ($1, $2, $3, $4)
		`, int64(experimentID), mapping.EvaluatorPort, string(mapping.SourceDirection), mapping.SourcePort); err != nil {
			return err
		}
	}
	return nil
}

func deleteDraftExperimentDefinition(ctx context.Context, tx Tx, id experiment.ID) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT binding.retention_claim_id
		FROM agent_experiment_fixture_bindings binding
		JOIN agent_experiment_fixtures fixture ON fixture.id = binding.fixture_id
		WHERE fixture.experiment_id = $1
	`, int64(id))
	if err != nil {
		return err
	}
	var claimIDs []int64
	for rows.Next() {
		var claimID int64
		if err := rows.Scan(&claimID); err != nil {
			_ = rows.Close()
			return err
		}
		claimIDs = append(claimIDs, claimID)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM agent_experiment_fixture_bindings
		WHERE fixture_id IN (SELECT id FROM agent_experiment_fixtures WHERE experiment_id = $1)
	`, int64(id)); err != nil {
		return err
	}
	for _, claimID := range claimIDs {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_snapshot_retention_claims WHERE id = $1`, claimID); err != nil {
			return err
		}
	}
	for _, table := range []string{
		"agent_experiment_evaluator_mappings", "agent_experiment_fixtures", "agent_experiment_variants",
	} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE experiment_id = $1`, int64(id)); err != nil {
			return err
		}
	}
	return nil
}

func loadStoredExperiment(
	ctx context.Context,
	queryer agentExperimentQueryer,
	teamID int,
	id experiment.ID,
) (experiment.StoredExperiment, error) {
	var stored experiment.StoredExperiment
	var state string
	var candidateSignature, evaluatorSignature []byte
	var evaluatorKind, evaluatorWorkflowName string
	var evaluatorDefinitionID int64
	var evaluatorVersion int
	var evaluatorFunctionID, evaluatorTargetConfigHash, evaluatorDevValidationProvenanceHash sql.NullString
	var evaluatorNodeParameters []byte
	var evaluatorMeasurementsPort string
	var startedAt, completedAt sql.NullTime
	err := queryer.QueryRowContext(ctx, `
		SELECT id, team_name, name, state, revision, candidate_signature, repetitions,
		       per_cell_budget_usd::double precision, total_budget_usd::double precision,
		       max_tokens_per_cell, evaluator_target_kind, evaluator_workflow_name,
		       evaluator_definition_id, evaluator_workflow_version, evaluator_function_id,
		       evaluator_node_parameters, evaluator_signature, evaluator_target_config_hash,
		       evaluator_dev_validation_provenance_hash,
		       evaluator_measurements_port, created_by,
		       created_at, updated_at, started_at, completed_at
		FROM agent_experiments
		WHERE id = $1 AND team_id = $2
	`, int64(id), teamID).Scan(
		&stored.ID, &stored.TeamName, &stored.Definition.Name, &state, &stored.Revision,
		&candidateSignature, &stored.Definition.Repetitions, &stored.Definition.Budget.PerCellUSD,
		&stored.Definition.Budget.TotalUSD, &stored.Definition.Budget.MaxTokensPerCell,
		&evaluatorKind, &evaluatorWorkflowName, &evaluatorDefinitionID, &evaluatorVersion,
		&evaluatorFunctionID, &evaluatorNodeParameters, &evaluatorSignature, &evaluatorTargetConfigHash,
		&evaluatorDevValidationProvenanceHash,
		&evaluatorMeasurementsPort, &stored.CreatedBy,
		&stored.CreatedAt, &stored.UpdatedAt, &startedAt, &completedAt,
	)
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	stored.Definition.State = experiment.State(state)
	if err := json.Unmarshal(candidateSignature, &stored.Definition.Signature); err != nil {
		return experiment.StoredExperiment{}, err
	}
	stored.Definition.Evaluator.Target = experiment.Target{
		Kind: experiment.TargetKind(evaluatorKind), WorkflowName: evaluatorWorkflowName,
		DefinitionID: evaluatorDefinitionID, Version: evaluatorVersion,
	}
	if evaluatorFunctionID.Valid {
		stored.Definition.Evaluator.Target.FunctionID = evaluatorFunctionID.String
	}
	if err := scanExperimentNodeParameters(
		evaluatorNodeParameters, &stored.Definition.Evaluator.Target,
	); err != nil {
		return experiment.StoredExperiment{}, err
	}
	if evaluatorTargetConfigHash.Valid {
		stored.Definition.Evaluator.TargetConfigHash = evaluatorTargetConfigHash.String
	}
	if evaluatorDevValidationProvenanceHash.Valid {
		stored.Definition.Evaluator.DevValidationProvenanceHash = evaluatorDevValidationProvenanceHash.String
	}
	if err := json.Unmarshal(evaluatorSignature, &stored.Definition.Evaluator.Signature); err != nil {
		return experiment.StoredExperiment{}, err
	}
	stored.Definition.Evaluator.MeasurementsPort = evaluatorMeasurementsPort
	if startedAt.Valid {
		stored.StartedAt = &startedAt.Time
	}
	if completedAt.Valid {
		stored.CompletedAt = &completedAt.Time
	}

	variantRows, err := queryer.QueryContext(ctx, `
		SELECT label, is_control, target_kind, workflow_name, definition_id,
		       workflow_version, function_id, node_parameters, signature_hash,
		       target_config_hash, dev_validation_provenance_hash
		FROM agent_experiment_variants
		WHERE experiment_id = $1 ORDER BY id
	`, int64(id))
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	for variantRows.Next() {
		var variant experiment.Variant
		var kind string
		var nodeParameters []byte
		var functionID, targetConfigHash, devValidationProvenanceHash sql.NullString
		if err := variantRows.Scan(&variant.Label, &variant.Control, &kind, &variant.Target.WorkflowName,
			&variant.Target.DefinitionID, &variant.Target.Version, &functionID, &nodeParameters,
			&variant.SignatureHash, &targetConfigHash, &devValidationProvenanceHash); err != nil {
			_ = variantRows.Close()
			return experiment.StoredExperiment{}, err
		}
		variant.Target.Kind = experiment.TargetKind(kind)
		if functionID.Valid {
			variant.Target.FunctionID = functionID.String
		}
		if err := scanExperimentNodeParameters(nodeParameters, &variant.Target); err != nil {
			_ = variantRows.Close()
			return experiment.StoredExperiment{}, err
		}
		if targetConfigHash.Valid {
			variant.TargetConfigHash = targetConfigHash.String
		}
		if devValidationProvenanceHash.Valid {
			variant.DevValidationProvenanceHash = devValidationProvenanceHash.String
		}
		stored.Definition.Variants = append(stored.Definition.Variants, variant)
	}
	if err := variantRows.Close(); err != nil {
		return experiment.StoredExperiment{}, err
	}

	fixtureRows, err := queryer.QueryContext(ctx, `
		SELECT id, label, role FROM agent_experiment_fixtures
		WHERE experiment_id = $1 ORDER BY id
	`, int64(id))
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	type loadedFixture struct {
		id      int64
		fixture experiment.Fixture
	}
	var fixtures []loadedFixture
	for fixtureRows.Next() {
		var value loadedFixture
		var role string
		if err := fixtureRows.Scan(&value.id, &value.fixture.Label, &role); err != nil {
			_ = fixtureRows.Close()
			return experiment.StoredExperiment{}, err
		}
		value.fixture.Role = experiment.FixtureRole(role)
		value.fixture.Inputs = make(map[string]snapshot.SnapshotID)
		fixtures = append(fixtures, value)
	}
	if err := fixtureRows.Close(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	for _, value := range fixtures {
		bindingRows, err := queryer.QueryContext(ctx, `
			SELECT port_name, snapshot_id FROM agent_experiment_fixture_bindings
			WHERE fixture_id = $1 ORDER BY port_name
		`, value.id)
		if err != nil {
			return experiment.StoredExperiment{}, err
		}
		for bindingRows.Next() {
			var port string
			var snapshotID int64
			if err := bindingRows.Scan(&port, &snapshotID); err != nil {
				_ = bindingRows.Close()
				return experiment.StoredExperiment{}, err
			}
			value.fixture.Inputs[port] = snapshot.SnapshotID(snapshotID)
		}
		if err := bindingRows.Close(); err != nil {
			return experiment.StoredExperiment{}, err
		}
		assertionRows, err := queryer.QueryContext(ctx, `
			SELECT metric_name, comparator, threshold_one, threshold_two
			FROM agent_experiment_control_assertions
			WHERE fixture_id = $1 ORDER BY metric_name
		`, value.id)
		if err != nil {
			return experiment.StoredExperiment{}, err
		}
		for assertionRows.Next() {
			var assertion experiment.Assertion
			var comparator string
			var thresholdOne float64
			var thresholdTwo sql.NullFloat64
			if err := assertionRows.Scan(&assertion.Metric, &comparator, &thresholdOne, &thresholdTwo); err != nil {
				_ = assertionRows.Close()
				return experiment.StoredExperiment{}, err
			}
			assertion.Comparator = experiment.Comparator(comparator)
			assertion.Thresholds = []float64{thresholdOne}
			if thresholdTwo.Valid {
				assertion.Thresholds = append(assertion.Thresholds, thresholdTwo.Float64)
			}
			value.fixture.Assertions = append(value.fixture.Assertions, assertion)
		}
		if err := assertionRows.Close(); err != nil {
			return experiment.StoredExperiment{}, err
		}
		stored.Definition.Fixtures = append(stored.Definition.Fixtures, value.fixture)
	}

	mappingRows, err := queryer.QueryContext(ctx, `
		SELECT evaluator_port, source_direction, source_port
		FROM agent_experiment_evaluator_mappings
		WHERE experiment_id = $1 ORDER BY evaluator_port
	`, int64(id))
	if err != nil {
		return experiment.StoredExperiment{}, err
	}
	for mappingRows.Next() {
		var mapping experiment.EvaluatorMapping
		var direction string
		if err := mappingRows.Scan(&mapping.EvaluatorPort, &direction, &mapping.SourcePort); err != nil {
			_ = mappingRows.Close()
			return experiment.StoredExperiment{}, err
		}
		mapping.SourceDirection = experiment.SourceDirection(direction)
		stored.Definition.Evaluator.Mappings = append(stored.Definition.Evaluator.Mappings, mapping)
	}
	if err := mappingRows.Close(); err != nil {
		return experiment.StoredExperiment{}, err
	}
	return stored, nil
}

func loadCandidateCell(ctx context.Context, queryer agentExperimentQueryer, cellID experiment.CellID) (experiment.CandidateCell, error) {
	var cell experiment.CandidateCell
	var kind string
	var nodeParameters []byte
	var functionID, targetConfigHash, devValidationProvenanceHash sql.NullString
	var resourceSourceAdmissionID sql.NullInt64
	var resourceSourceAssociationCount int
	err := queryer.QueryRowContext(ctx, `
		SELECT cell.id, cell.experiment_id, cell.fixture_id, cell.variant_id,
		       source.resource_source_admission_id, source.association_count,
		       experiment.team_id, experiment.team_name, experiment.created_by,
		       cell.repetition, variant.target_kind, variant.workflow_name,
		       variant.definition_id, variant.workflow_version, variant.function_id,
		       variant.node_parameters,
		       variant.target_config_hash, variant.dev_validation_provenance_hash,
		       experiment.per_cell_budget_usd::double precision,
		       experiment.total_budget_usd::double precision,
		       experiment.max_tokens_per_cell
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		JOIN agent_experiment_variants variant ON variant.id = cell.variant_id
		LEFT JOIN LATERAL (
			SELECT count(*)::integer AS association_count,
			       min(resource_source_admission_id) AS resource_source_admission_id
			FROM agent_experiment_resource_source_admissions source
			WHERE source.experiment_id = cell.experiment_id
			  AND source.team_id = experiment.team_id
			  AND source.workflow_definition_id = variant.definition_id
		) source ON true
		WHERE cell.id = $1
	`, int64(cellID)).Scan(&cell.ID, &cell.ExperimentID, &cell.FixtureID, &cell.VariantID,
		&resourceSourceAdmissionID, &resourceSourceAssociationCount,
		&cell.TeamID, &cell.TeamName, &cell.CreatedBy, &cell.Repetition, &kind,
		&cell.Target.WorkflowName, &cell.Target.DefinitionID, &cell.Target.Version, &functionID,
		&nodeParameters,
		&targetConfigHash, &devValidationProvenanceHash, &cell.Budget.PerCellUSD, &cell.Budget.TotalUSD,
		&cell.Budget.MaxTokensPerCell)
	if err != nil {
		return experiment.CandidateCell{}, err
	}
	if resourceSourceAssociationCount > 1 {
		return experiment.CandidateCell{}, fmt.Errorf(
			"db: experiment candidate cell has ambiguous resource source admissions",
		)
	}
	if resourceSourceAssociationCount == 1 {
		if !resourceSourceAdmissionID.Valid || resourceSourceAdmissionID.Int64 <= 0 {
			return experiment.CandidateCell{}, fmt.Errorf(
				"db: experiment candidate source admission is invalid",
			)
		}
		value := resourceSourceAdmissionID.Int64
		cell.ResourceSourceAdmissionID = &value
	}
	cell.Target.Kind = experiment.TargetKind(kind)
	if functionID.Valid {
		cell.Target.FunctionID = functionID.String
	}
	// The dispatch seam: this is the value the binder instantiates the node
	// with. Dropping it here would launch every node variant with defaults and
	// grade two identical cells against each other.
	if err := scanExperimentNodeParameters(nodeParameters, &cell.Target); err != nil {
		return experiment.CandidateCell{}, err
	}
	if targetConfigHash.Valid {
		cell.TargetConfigHash = targetConfigHash.String
	}
	if devValidationProvenanceHash.Valid {
		cell.DevValidationProvenanceHash = devValidationProvenanceHash.String
	}
	cell.Inputs, err = loadFixtureInputs(ctx, queryer, cell.FixtureID)
	return cell, err
}

func loadEvaluationCell(ctx context.Context, queryer agentExperimentQueryer, cellID experiment.CellID) (experiment.EvaluationCell, error) {
	var cell experiment.EvaluationCell
	var candidateSignature, evaluatorSignature []byte
	var evaluatorKind string
	var evaluatorNodeParameters []byte
	var evaluatorFunctionID, evaluatorTargetConfigHash, evaluatorDevValidationProvenanceHash sql.NullString
	var evaluatorRunID, resourceSourceAdmissionID sql.NullInt64
	var resourceSourceAssociationCount int
	var role string
	err := queryer.QueryRowContext(ctx, `
		SELECT cell.id, cell.experiment_id, experiment.team_id, experiment.team_name,
		       experiment.created_by, cell.candidate_workflow_run_id,
		       source.resource_source_admission_id, source.association_count,
		       evaluation.evaluator_workflow_run_id, experiment.candidate_signature,
		       experiment.evaluator_target_kind, experiment.evaluator_workflow_name,
		       experiment.evaluator_definition_id, experiment.evaluator_workflow_version,
		       experiment.evaluator_function_id, experiment.evaluator_node_parameters,
		       experiment.evaluator_signature,
		       experiment.evaluator_target_config_hash, experiment.evaluator_dev_validation_provenance_hash,
		       experiment.evaluator_measurements_port, fixture.id, fixture.role,
		       experiment.per_cell_budget_usd::double precision,
		       experiment.total_budget_usd::double precision,
		       experiment.max_tokens_per_cell
		FROM agent_experiment_cells cell
		JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
		JOIN agent_experiment_fixtures fixture ON fixture.id = cell.fixture_id
		LEFT JOIN LATERAL (
			SELECT count(*)::integer AS association_count,
			       min(resource_source_admission_id) AS resource_source_admission_id
			FROM agent_experiment_resource_source_admissions source
			WHERE source.experiment_id = cell.experiment_id
			  AND source.team_id = experiment.team_id
			  AND source.workflow_definition_id = experiment.evaluator_definition_id
		) source ON true
		LEFT JOIN agent_experiment_evaluations evaluation ON evaluation.cell_id = cell.id
		WHERE cell.id = $1
	`, int64(cellID)).Scan(&cell.ID, &cell.ExperimentID, &cell.TeamID, &cell.TeamName,
		&cell.CreatedBy, &cell.CandidateWorkflowRunID, &resourceSourceAdmissionID,
		&resourceSourceAssociationCount, &evaluatorRunID, &candidateSignature,
		&evaluatorKind, &cell.Evaluator.Target.WorkflowName, &cell.Evaluator.Target.DefinitionID,
		&cell.Evaluator.Target.Version, &evaluatorFunctionID, &evaluatorNodeParameters,
		&evaluatorSignature,
		&evaluatorTargetConfigHash, &evaluatorDevValidationProvenanceHash,
		&cell.Evaluator.MeasurementsPort, new(int64), &role,
		&cell.Budget.PerCellUSD, &cell.Budget.TotalUSD, &cell.Budget.MaxTokensPerCell)
	if err != nil {
		return experiment.EvaluationCell{}, err
	}
	if resourceSourceAssociationCount > 1 {
		return experiment.EvaluationCell{}, fmt.Errorf(
			"db: experiment evaluator cell has ambiguous resource source admissions",
		)
	}
	if resourceSourceAssociationCount == 1 {
		if !resourceSourceAdmissionID.Valid || resourceSourceAdmissionID.Int64 <= 0 {
			return experiment.EvaluationCell{}, fmt.Errorf(
				"db: experiment evaluator source admission is invalid",
			)
		}
		value := resourceSourceAdmissionID.Int64
		cell.ResourceSourceAdmissionID = &value
	}
	cell.Evaluator.Target.Kind = experiment.TargetKind(evaluatorKind)
	if evaluatorFunctionID.Valid {
		cell.Evaluator.Target.FunctionID = evaluatorFunctionID.String
	}
	if err := scanExperimentNodeParameters(
		evaluatorNodeParameters, &cell.Evaluator.Target,
	); err != nil {
		return experiment.EvaluationCell{}, err
	}
	if evaluatorTargetConfigHash.Valid {
		cell.Evaluator.TargetConfigHash = evaluatorTargetConfigHash.String
	}
	if evaluatorDevValidationProvenanceHash.Valid {
		cell.Evaluator.DevValidationProvenanceHash = evaluatorDevValidationProvenanceHash.String
	}
	if evaluatorRunID.Valid {
		value := snapshot.WorkflowRunID(evaluatorRunID.Int64)
		cell.EvaluatorWorkflowRunID = &value
	}
	if err := json.Unmarshal(candidateSignature, &cell.CandidateSignature); err != nil {
		return experiment.EvaluationCell{}, err
	}
	if err := json.Unmarshal(evaluatorSignature, &cell.Evaluator.Signature); err != nil {
		return experiment.EvaluationCell{}, err
	}
	var fixtureID int64
	if err := queryer.QueryRowContext(ctx, `SELECT fixture_id FROM agent_experiment_cells WHERE id = $1`, int64(cellID)).Scan(&fixtureID); err != nil {
		return experiment.EvaluationCell{}, err
	}
	cell.FixtureInputs, err = loadFixtureInputs(ctx, queryer, fixtureID)
	if err != nil {
		return experiment.EvaluationCell{}, err
	}
	cell.Role = experiment.FixtureRole(role)
	cell.Assertions, err = loadFixtureAssertions(ctx, queryer, fixtureID)
	if err != nil {
		return experiment.EvaluationCell{}, err
	}
	rows, err := queryer.QueryContext(ctx, `
		SELECT evaluator_port, source_direction, source_port
		FROM agent_experiment_evaluator_mappings
		WHERE experiment_id = $1 ORDER BY evaluator_port
	`, int64(cell.ExperimentID))
	if err != nil {
		return experiment.EvaluationCell{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var mapping experiment.EvaluatorMapping
		var direction string
		if err := rows.Scan(&mapping.EvaluatorPort, &direction, &mapping.SourcePort); err != nil {
			return experiment.EvaluationCell{}, err
		}
		mapping.SourceDirection = experiment.SourceDirection(direction)
		cell.Evaluator.Mappings = append(cell.Evaluator.Mappings, mapping)
	}
	return cell, rows.Err()
}

func loadFixtureInputs(ctx context.Context, queryer agentExperimentQueryer, fixtureID int64) (map[string]snapshot.SnapshotID, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT port_name, snapshot_id FROM agent_experiment_fixture_bindings
		WHERE fixture_id = $1 ORDER BY port_name
	`, fixtureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make(map[string]snapshot.SnapshotID)
	for rows.Next() {
		var port string
		var id int64
		if err := rows.Scan(&port, &id); err != nil {
			return nil, err
		}
		values[port] = snapshot.SnapshotID(id)
	}
	return values, rows.Err()
}

func loadFixtureAssertions(ctx context.Context, queryer agentExperimentQueryer, fixtureID int64) ([]experiment.Assertion, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT metric_name, comparator, threshold_one, threshold_two
		FROM agent_experiment_control_assertions WHERE fixture_id = $1 ORDER BY metric_name
	`, fixtureID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []experiment.Assertion
	for rows.Next() {
		var value experiment.Assertion
		var comparator string
		var one float64
		var two sql.NullFloat64
		if err := rows.Scan(&value.Metric, &comparator, &one, &two); err != nil {
			return nil, err
		}
		value.Comparator = experiment.Comparator(comparator)
		value.Thresholds = []float64{one}
		if two.Valid {
			value.Thresholds = append(value.Thresholds, two.Float64)
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

const storedCellsQuery = `
	SELECT cell.id, cell.experiment_id, fixture.id, fixture.label, fixture.role,
	       variant.id, variant.label, cell.repetition, cell.status,
	       cell.candidate_workflow_run_id, evaluation.evaluator_workflow_run_id,
	       evaluation.measurement_snapshot_id, cell.candidate_failure_category,
	       cell.created_at, cell.updated_at, cell.completed_at
	FROM agent_experiment_cells cell
	JOIN agent_experiments experiment ON experiment.id = cell.experiment_id
	JOIN agent_experiment_fixtures fixture ON fixture.id = cell.fixture_id
	JOIN agent_experiment_variants variant ON variant.id = cell.variant_id
	LEFT JOIN agent_experiment_evaluations evaluation ON evaluation.cell_id = cell.id
`

type rowScanner interface {
	Scan(...any) error
}

func scanStoredExperimentCell(scanner rowScanner) (experiment.StoredCell, error) {
	var value experiment.StoredCell
	var role, status string
	var candidateRunID, evaluatorRunID, measurementID sql.NullInt64
	var failure sql.NullString
	var completedAt sql.NullTime
	err := scanner.Scan(&value.ID, &value.ExperimentID, &value.FixtureID, &value.FixtureLabel,
		&role, &value.VariantID, &value.VariantLabel, &value.Repetition, &status,
		&candidateRunID, &evaluatorRunID, &measurementID, &failure,
		&value.CreatedAt, &value.UpdatedAt, &completedAt)
	if err != nil {
		return experiment.StoredCell{}, err
	}
	value.FixtureRole = experiment.FixtureRole(role)
	value.Status = experiment.CellStatus(status)
	if candidateRunID.Valid {
		run := snapshot.WorkflowRunID(candidateRunID.Int64)
		value.CandidateWorkflowRunID = &run
	}
	if evaluatorRunID.Valid {
		run := snapshot.WorkflowRunID(evaluatorRunID.Int64)
		value.EvaluatorWorkflowRunID = &run
	}
	if measurementID.Valid {
		id := snapshot.SnapshotID(measurementID.Int64)
		value.MeasurementSnapshotID = &id
	}
	if failure.Valid {
		value.CandidateFailureCategory = failure.String
	}
	if completedAt.Valid {
		value.CompletedAt = &completedAt.Time
	}
	return value, nil
}

func scanExperimentCellIDs(rows *sql.Rows) ([]experiment.CellID, error) {
	defer rows.Close()
	var ids []experiment.CellID
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, experiment.CellID(id))
	}
	return ids, rows.Err()
}

func lockExperiment(ctx context.Context, tx Tx, teamID int, id experiment.ID) (experiment.State, int64, error) {
	var state experiment.State
	var revision int64
	err := tx.QueryRowContext(ctx, `
		SELECT state, revision FROM agent_experiments
		WHERE id = $1 AND team_id = $2 FOR UPDATE
	`, int64(id), teamID).Scan(&state, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, experiment.ErrNotFound
	}
	return state, revision, err
}

func completeExperimentIfTerminalTx(ctx context.Context, tx Tx, cellID experiment.CellID) error {
	var experimentID experiment.ID
	var teamID int
	err := tx.QueryRowContext(ctx, `
		SELECT experiment.id, experiment.team_id
		FROM agent_experiments experiment
		JOIN agent_experiment_cells completed_cell
		  ON completed_cell.experiment_id = experiment.id
		WHERE completed_cell.id = $1
		  AND experiment.state = 'running'
		  AND experiment.frozen_scorecard IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM agent_experiment_cells cell
			WHERE cell.experiment_id = experiment.id AND cell.status IN ('pending', 'running')
		  )
		FOR UPDATE OF experiment
	`, int64(cellID)).Scan(&experimentID, &teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	frozenScorecard, err := marshalExperimentScorecard(ctx, tx, teamID, experimentID)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_experiments
		SET state = 'completed', completed_at = now(), updated_at = now(),
		    revision = revision + 1, frozen_scorecard = $3
		WHERE id = $1 AND team_id = $2 AND state = 'running'
		  AND frozen_scorecard IS NULL
	`, int64(experimentID), teamID, frozenScorecard)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("db: failed to atomically complete and freeze experiment scorecard")
	}
	return nil
}

func validateExperimentMutation(
	ctx context.Context,
	teamID int,
	teamName string,
	actor string,
	definition experiment.Definition,
) error {
	if ctx == nil || teamID <= 0 || strings.TrimSpace(teamName) == "" || !validExperimentActor(actor) {
		return experiment.ErrInvalidDefinition
	}
	if definition.State != experiment.StateDraft {
		return fmt.Errorf("%w: experiment state must be draft", experiment.ErrInvalidDefinition)
	}
	if err := definition.Validate(); err != nil {
		return fmt.Errorf("%w: %v", experiment.ErrInvalidDefinition, err)
	}
	return nil
}

func validExperimentActor(actor string) bool {
	return actor != "" && actor == strings.TrimSpace(actor) && len(actor) <= 256
}

func validateAuthoritativeExperimentTargets(
	ctx context.Context,
	queryer agentExperimentQueryer,
	definition experiment.Definition,
) error {
	for index, variant := range definition.Variants {
		target, err := loadAuthoritativeExperimentTarget(ctx, queryer, variant.Target)
		if err != nil {
			return fmt.Errorf("%w: variants[%d] target: %v", experiment.ErrInvalidDefinition, index, err)
		}
		if !target.Signature.Equal(definition.Signature) {
			return fmt.Errorf("%w: variant %q signature does not match its immutable workflow target", experiment.ErrInvalidDefinition, variant.Label)
		}
		hash, err := experiment.HashSignature(target.Signature)
		if err != nil {
			return fmt.Errorf("%w: variant %q target signature: %v", experiment.ErrInvalidDefinition, variant.Label, err)
		}
		if variant.SignatureHash != hash {
			return fmt.Errorf("%w: variant %q signature hash does not match its immutable workflow target", experiment.ErrInvalidDefinition, variant.Label)
		}
	}

	evaluator, err := loadAuthoritativeExperimentTarget(ctx, queryer, definition.Evaluator.Target)
	if err != nil {
		return fmt.Errorf("%w: evaluator target: %v", experiment.ErrInvalidDefinition, err)
	}
	if !evaluator.Signature.Equal(definition.Evaluator.Signature) {
		return fmt.Errorf("%w: evaluator signature does not match its immutable workflow target", experiment.ErrInvalidDefinition)
	}
	return nil
}

func freezeAuthoritativeExperimentTargets(
	ctx context.Context,
	queryer agentExperimentQueryer,
	definition experiment.Definition,
	renderer experiment.TargetRenderer,
	globalCapEnabled bool,
) ([]frozenExperimentTarget, frozenExperimentTarget, error) {
	if renderer == nil {
		return nil, frozenExperimentTarget{}, fmt.Errorf("%w: trusted experiment target renderer is unavailable", experiment.ErrInvalidDefinition)
	}
	render := func(label string, requested experiment.Target) (workflow.RenderedFunction, frozenExperimentTarget, error) {
		target, err := loadAuthoritativeExperimentTarget(ctx, queryer, requested)
		if err != nil {
			return workflow.RenderedFunction{}, frozenExperimentTarget{}, fmt.Errorf("%w: %s target: %v", experiment.ErrInvalidDefinition, label, err)
		}
		rendered, err := renderer.RenderFunction(target)
		if err != nil {
			return workflow.RenderedFunction{}, frozenExperimentTarget{}, fmt.Errorf("%w: %s target dependencies are not renderable: %v", experiment.ErrInvalidDefinition, label, err)
		}
		hash, err := workflow.RenderedTargetConfigHash(rendered.Config, rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash)
		if err != nil || rendered.TargetConfigHash != hash {
			return workflow.RenderedFunction{}, frozenExperimentTarget{}, fmt.Errorf("%w: %s rendered target config identity is invalid", experiment.ErrInvalidDefinition, label)
		}
		name, err := workflow.TemplateName(target.Kind, target.WorkflowName, target.WorkflowVersion, target.FunctionID, hash)
		if err != nil || rendered.TemplateName != name || !target.Signature.Equal(rendered.TargetSignature) {
			return workflow.RenderedFunction{}, frozenExperimentTarget{}, fmt.Errorf("%w: %s rendered target identity is invalid", experiment.ErrInvalidDefinition, label)
		}
		if err := workflow.ValidateDevValidationAuthority(rendered.DevValidationProfiles, rendered.DevValidationProvenanceHash); err != nil ||
			rendered.DevValidationProvenanceHash != target.DevValidationProvenanceHash ||
			rendered.DevValidationProvenanceHash != target.Function.DevValidationProvenanceHash {
			return workflow.RenderedFunction{}, frozenExperimentTarget{}, fmt.Errorf("%w: %s rendered dev validation authority is invalid", experiment.ErrInvalidDefinition, label)
		}
		return rendered, frozenExperimentTarget{targetConfigHash: rendered.TargetConfigHash, devValidationProvenanceHash: rendered.DevValidationProvenanceHash}, nil
	}

	variants := make([]frozenExperimentTarget, len(definition.Variants))
	candidateConfigs := make([]atc.Config, len(definition.Variants))
	for index, variant := range definition.Variants {
		rendered, frozen, err := render("variant "+variant.Label, variant.Target)
		if err != nil {
			return nil, frozenExperimentTarget{}, err
		}
		variants[index] = frozen
		candidateConfigs[index] = rendered.Config
	}
	evaluator, frozenEvaluator, err := render("evaluator", definition.Evaluator.Target)
	if err != nil {
		return nil, frozenExperimentTarget{}, err
	}
	if err := experiment.ValidateExecutionBudgetsForGlobalCap(
		definition.Budget,
		definition.ExpectedCells(),
		candidateConfigs,
		evaluator.Config,
		globalCapEnabled,
	); err != nil {
		return nil, frozenExperimentTarget{}, fmt.Errorf("%w: %v", experiment.ErrInvalidDefinition, err)
	}
	return variants, frozenEvaluator, nil
}

func loadAuthoritativeExperimentTarget(
	ctx context.Context,
	queryer agentExperimentQueryer,
	target experiment.Target,
) (workflow.FunctionTarget, error) {
	// A node target binds a node definition, which the workflow branch below
	// cannot load: it filters definition_kind = 'workflow' and compiles a
	// workflow manifest. Branching here rather than widening that filter keeps
	// a node from ever being resolved through the workflow compiler.
	if target.Kind == experiment.TargetNode {
		return loadAuthoritativeExperimentNodeTarget(ctx, queryer, target)
	}
	var definition workflow.Definition
	var rawYAML string
	var manifestJSON sql.NullString
	var compiledJSON sql.NullString
	err := queryer.QueryRowContext(ctx, `
		SELECT id, name, version, content_hash, schema_version, signature_version,
		       definition, source_manifest, compiled_definition
		FROM agent_workflow_definitions
		WHERE id = $1 AND definition_kind = 'workflow'
	`, target.DefinitionID).Scan(&definition.ID, &definition.Name, &definition.Version,
		&definition.ContentHash, &definition.SchemaVersion, &definition.SignatureVersion,
		&rawYAML, &manifestJSON, &compiledJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.FunctionTarget{}, fmt.Errorf("definition_id %d does not exist", target.DefinitionID)
	}
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	if definition.Name != target.WorkflowName || definition.Version != target.Version {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"definition_id %d is %s/v%d, not %s/v%d",
			target.DefinitionID, definition.Name, definition.Version, target.WorkflowName, target.Version,
		)
	}
	if definition.SchemaVersion != 3 {
		return workflow.FunctionTarget{}, fmt.Errorf("definition_id %d is schema_version %d, not 3", target.DefinitionID, definition.SchemaVersion)
	}
	compiled, source, err := compileStoredWorkflowSource(definition.Name, definition.Version, definition.ContentHash, rawYAML, manifestJSON, compiledJSON)
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	metadata, err := compiled.VersionMetadata()
	if err != nil || metadata.SchemaVersion != definition.SchemaVersion || metadata.SignatureVersion != definition.SignatureVersion {
		return workflow.FunctionTarget{}, fmt.Errorf("stored workflow metadata does not match its compiled source")
	}
	populateCompiledWorkflowDefinition(&definition, compiled, source)

	var resolved workflow.FunctionTarget
	switch target.Kind {
	case experiment.TargetWorkflow:
		resolved, err = workflow.FullFunctionTarget(definition)
	case experiment.TargetFunction:
		resolved, err = workflow.ExtractFunctionTarget(definition, target.FunctionID)
	default:
		return workflow.FunctionTarget{}, fmt.Errorf("unsupported target kind %q", target.Kind)
	}
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	if resolved.WorkflowDefinitionID != int(target.DefinitionID) ||
		resolved.WorkflowName != target.WorkflowName || resolved.WorkflowVersion != target.Version ||
		resolved.FunctionID != target.FunctionID || string(resolved.Kind) != string(target.Kind) {
		return workflow.FunctionTarget{}, fmt.Errorf("resolved workflow target identity does not match the requested immutable identity")
	}
	return resolved, nil
}

// loadAuthoritativeExperimentNodeTarget freezes a reusable node target from
// durable authority. It deliberately mirrors workflowrun's
// executableNodeDefinition rather than sharing it: that function lives behind
// the binder's node store, and the freeze must read the same row the runner
// will later bind, in this transaction, under this queryer.
//
// The instantiated function -- and therefore the target config hash frozen
// from it -- depends on target.NodeParameters, which is exactly why two node
// variants that differ only in a parameter are distinguishable at all.
func loadAuthoritativeExperimentNodeTarget(
	ctx context.Context,
	queryer agentExperimentQueryer,
	target experiment.Target,
) (workflow.FunctionTarget, error) {
	var (
		id                              int
		name                            string
		version                         int
		contentHash                     string
		schemaVersion, signatureVersion int
		manifestJSON                    sql.NullString
		compiledJSON                    sql.NullString
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT id, name, version, content_hash, schema_version, signature_version,
		       source_manifest, compiled_definition
		FROM agent_workflow_definitions
		WHERE id = $1 AND definition_kind = 'node'
	`, target.DefinitionID).Scan(&id, &name, &version, &contentHash,
		&schemaVersion, &signatureVersion, &manifestJSON, &compiledJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return workflow.FunctionTarget{}, fmt.Errorf("node definition_id %d does not exist", target.DefinitionID)
	}
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	if name != target.WorkflowName || version != target.Version {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"node definition_id %d is %s/v%d, not %s/v%d",
			target.DefinitionID, name, version, target.WorkflowName, target.Version,
		)
	}
	if schemaVersion != nodeRuntimeSchemaVersion {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"node definition_id %d is schema_version %d, not %d",
			target.DefinitionID, schemaVersion, nodeRuntimeSchemaVersion,
		)
	}
	// A node with no persisted compiled form cannot be frozen. Recompiling it
	// here would silently substitute today's broker authority for the one it
	// was imported under, which is the opposite of a frozen identity.
	if !compiledJSON.Valid || !manifestJSON.Valid {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"node definition_id %d has no persisted compiled source to freeze", target.DefinitionID,
		)
	}
	var source workflow.Manifest
	if err := json.Unmarshal([]byte(manifestJSON.String), &source); err != nil {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"node definition_id %d has an undecodable source manifest: %w", target.DefinitionID, err,
		)
	}
	compiled, err := workflow.ParseCompiledNodeDefinition([]byte(compiledJSON.String))
	if err != nil {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"node definition_id %d has an invalid compiled definition: %w", target.DefinitionID, err,
		)
	}
	if compiled.Name != name || source.Hash() != contentHash ||
		signatureVersion != compiled.Function.SignatureVersion {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"stored node metadata for definition_id %d does not match its compiled source",
			target.DefinitionID,
		)
	}
	function, err := compiled.Instantiate(target.NodeParameters)
	if err != nil {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"node definition_id %d cannot be instantiated with the requested parameters: %w",
			target.DefinitionID, err,
		)
	}
	if function.SignatureVersion != signatureVersion {
		return workflow.FunctionTarget{}, fmt.Errorf(
			"instantiated node definition_id %d does not match its durable signature version",
			target.DefinitionID,
		)
	}
	resolved, err := workflow.FullFunctionTarget(workflow.Definition{
		ID: id, Name: name, Version: version, ContentHash: contentHash,
		SchemaVersion: schemaVersion, SignatureVersion: signatureVersion,
		Compiled: workflow.CompiledDefinition{
			SchemaVersion: schemaVersion, Name: name, Function: function,
		},
	})
	if err != nil {
		return workflow.FunctionTarget{}, err
	}
	// A node instantiates as the whole executable surface of its definition,
	// so it renders through the same full-workflow path a workflow target does
	// and carries no function ID. The kinds are checked against that mapping
	// rather than compared as strings, which would reject every node target.
	if resolved.WorkflowDefinitionID != int(target.DefinitionID) ||
		resolved.WorkflowName != target.WorkflowName || resolved.WorkflowVersion != target.Version ||
		resolved.FunctionID != "" || target.FunctionID != "" ||
		resolved.Kind != workflow.TargetWorkflow {
		return workflow.FunctionTarget{}, fmt.Errorf("resolved node target identity does not match the requested immutable identity")
	}
	return resolved, nil
}

func validateStoredExperimentFixturesAvailable(
	ctx context.Context,
	queryer agentExperimentQueryer,
	teamID int,
	id experiment.ID,
) error {
	var unavailable int
	err := queryer.QueryRowContext(ctx, `
		SELECT count(*)
		FROM agent_experiment_fixture_bindings binding
		JOIN agent_experiment_fixtures fixture ON fixture.id = binding.fixture_id
		JOIN agent_snapshots snapshot ON snapshot.id = binding.snapshot_id
		JOIN agent_snapshot_retention_claims claim ON claim.id = binding.retention_claim_id
		WHERE fixture.experiment_id = $1
		  AND (snapshot.content_state <> 'available' OR claim.team_id <> $2
		       OR (claim.expires_at IS NOT NULL AND claim.expires_at <= now()))
	`, int64(id), teamID).Scan(&unavailable)
	if err != nil {
		return err
	}
	if unavailable != 0 {
		return fmt.Errorf("%w: one or more retained fixture snapshots are unavailable", experiment.ErrInvalidDefinition)
	}
	return nil
}

func marshalExperimentSignatures(definition experiment.Definition) ([]byte, []byte, error) {
	candidate, err := json.Marshal(definition.Signature)
	if err != nil {
		return nil, nil, err
	}
	evaluator, err := json.Marshal(definition.Evaluator.Signature)
	return candidate, evaluator, err
}

// marshalExperimentNodeParameters renders a target's frozen node parameters
// for durable storage. The column is NOT NULL, so an absent map stores as the
// empty object rather than SQL NULL: "this target declares no parameters" and
// "this target's parameters were never written" must not share a durable
// representation, because only the second is a bug and it is the one that
// silently degrades an A/B into a comparison of two identical cells.
func marshalExperimentNodeParameters(target experiment.Target) ([]byte, error) {
	values := target.NodeParameters
	if values == nil {
		values = map[string]string{}
	}
	return json.Marshal(values)
}

// scanExperimentNodeParameters restores them onto a target read back from the
// database. An empty object decodes to a nil map so that a round trip is
// value-identical to a target that never carried parameters; nothing in the
// experiment or binder contracts distinguishes nil from empty.
func scanExperimentNodeParameters(raw []byte, target *experiment.Target) error {
	if len(raw) == 0 {
		return nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return fmt.Errorf("db: stored experiment node parameters are not a string map: %w", err)
	}
	if len(values) == 0 {
		return nil
	}
	target.NodeParameters = values
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func terminalExperimentCellStatus(status experiment.CellStatus) bool {
	switch status {
	case experiment.CellValidMeasurement, experiment.CellCandidateContractFailure,
		experiment.CellCandidatePlatformFailure, experiment.CellEvaluatorFailure,
		experiment.CellNegativeControlFailure, experiment.CellSkippedBudget, experiment.CellCanceled:
		return true
	default:
		return false
	}
}

func nullableSnapshotIDEqual(value sql.NullInt64, expected *snapshot.SnapshotID) bool {
	if expected == nil {
		return !value.Valid
	}
	return value.Valid && value.Int64 == int64(*expected)
}

// Keep the public signature type referenced here so accidental changes to its
// persisted JSON shape are caught at compile time alongside this factory.
var _ workflow.PublicSignature
