package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workflow"
	"github.com/concourse/concourse/atc"
)

// WorkflowResourceSourcePipeline is the durable ownership record for one
// ordinary source-selection pipeline. Its state is deliberately independent
// from pipeline.archived so only this factory can transition its lifecycle.
type WorkflowResourceSourcePipeline struct {
	PipelineID            int
	TeamID                int
	PRBindingID           *int64
	WorkflowDefinitionID  int
	WorkflowName          string
	WorkflowVersion       int
	PipelineConfigVersion int
	ConfigHash            string
	SourceDeclarations    []ResourceSourceDeclaration
	State                 AgentWorkflowResourceSourcePipelineState
}

// ResourceSourceDeclaration is frozen alongside the pipeline registration so
// the admission writer can prove a selecting build maps to its declared port.
type ResourceSourceDeclaration struct {
	SourceName   string           `json:"source_name"`
	ResourceName string           `json:"resource_name"`
	SnapshotType snapshot.TypeRef `json:"snapshot_type"`
}

// WorkflowResourceSourcePipelineRenderer derives an immutable source
// selection pipeline from the exact persisted workflow revision being
// promoted. It deliberately has no caller supplied config input.
type WorkflowResourceSourcePipelineRenderer interface {
	RenderResourceSourcePipelineForPromotion(workflow.Definition, int) (workflow.RenderedResourceSourcePipeline, bool, error)
}

type DefaultWorkflowResourceSourcePipelineRenderer struct{}

func (DefaultWorkflowResourceSourcePipelineRenderer) RenderResourceSourcePipelineForPromotion(
	definition workflow.Definition,
	teamID int,
) (workflow.RenderedResourceSourcePipeline, bool, error) {
	if definition.Compiled.Function == nil || len(definition.Compiled.Function.ResourceSources) == 0 {
		return workflow.RenderedResourceSourcePipeline{}, false, nil
	}
	target, err := workflow.ResourceSourcePipelineTargetFor(definition, teamID)
	if err != nil {
		return workflow.RenderedResourceSourcePipeline{}, false, err
	}
	rendered, err := workflow.RenderResourceSourcePipeline(target)
	if err != nil {
		return workflow.RenderedResourceSourcePipeline{}, false, err
	}
	return rendered, true, nil
}

// WorkflowResourceSourcePipelineRegistry shares the workflow-promotion
// transaction. The source pipeline registry and live revision therefore
// become visible together, rather than admitting a build from an orphaned
// physical pipeline.
type WorkflowResourceSourcePipelineRegistry interface {
	SaveAndActivateForPromotion(context.Context, Tx, int, workflow.Definition, workflow.RenderedResourceSourcePipeline) (WorkflowResourceSourcePipeline, error)
	DrainActiveForPromotion(context.Context, Tx, int, string) (bool, error)
}

type WorkflowResourceSourcePipelinesFactory interface {
	WorkflowResourceSourcePipelineRegistry
	Activate(context.Context, WorkflowResourceSourcePipeline) error
	Drain(context.Context, int, int) error
	Archive(context.Context, int, int) error
	Find(context.Context, int, int) (WorkflowResourceSourcePipeline, bool, error)
	FindActive(context.Context, int, string) (WorkflowResourceSourcePipeline, bool, error)
	ResourceSourcePipelineLifecycle(context.Context, int) ([]AgentWorkflowResourceSourcePipelineLifecycle, error)
	UnpauseActiveResourceSourcePipeline(context.Context, int, WorkflowResourceSourcePipeline) (bool, error)
	PauseDrainedResourceSourcePipeline(context.Context, int, WorkflowResourceSourcePipeline) (bool, error)
	ArchiveDrainedResourceSourcePipeline(context.Context, int, WorkflowResourceSourcePipeline) (bool, error)
}

type workflowResourceSourcePipelinesFactory struct{ conn DbConn }

func NewWorkflowResourceSourcePipelinesFactory(conn DbConn) WorkflowResourceSourcePipelinesFactory {
	return &workflowResourceSourcePipelinesFactory{conn: conn}
}

// Activate registers an already-persisted ordinary physical pipeline as the
// one active source-selection pipeline for its workflow name, draining any
// earlier revision inside the same transaction.
func (factory *workflowResourceSourcePipelinesFactory) Activate(ctx context.Context, pipeline WorkflowResourceSourcePipeline) error {
	if pipeline.PRBindingID != nil {
		return fmt.Errorf("db: definition-owned source pipeline cannot carry a PR binding")
	}
	if pipeline.State != "" && pipeline.State != AgentWorkflowResourceSourcePipelineActive {
		return fmt.Errorf("db: invalid workflow resource source pipeline activation state")
	}
	if err := validateWorkflowResourceSourcePipeline(pipeline); err != nil {
		return err
	}
	pipeline.State = "active"
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pipelines WHERE id=$1 AND team_id=$2 AND NOT template AND NOT archived AND version=$3)`, pipeline.PipelineID, pipeline.TeamID, pipeline.PipelineConfigVersion).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("db: workflow resource source pipeline is not an active ordinary pipeline at its claimed config version")
	}
	// Drain a previous revision before making this one active. This transaction
	// leaves at most one active admission pipeline for the workflow name.
	if _, err := tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='draining', updated_at=now() WHERE team_id=$1 AND workflow_name=$2 AND state='active' AND pipeline_id<>$3 AND pr_binding_id IS NULL`, pipeline.TeamID, pipeline.WorkflowName, pipeline.PipelineID); err != nil {
		return err
	}
	declarations, err := json.Marshal(pipeline.SourceDeclarations)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_workflow_resource_source_pipelines (pipeline_id, team_id, workflow_definition_id, workflow_name, workflow_version, pipeline_config_version, config_hash, source_declarations, state) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active') ON CONFLICT (pipeline_id) DO NOTHING`, pipeline.PipelineID, pipeline.TeamID, pipeline.WorkflowDefinitionID, pipeline.WorkflowName, pipeline.WorkflowVersion, pipeline.PipelineConfigVersion, pipeline.ConfigHash, declarations)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var stored WorkflowResourceSourcePipeline
		var storedDeclarations []byte
		err = tx.QueryRowContext(ctx, `SELECT pipeline_id, team_id, workflow_definition_id, workflow_name, workflow_version, pipeline_config_version, config_hash, source_declarations, state FROM agent_workflow_resource_source_pipelines WHERE pipeline_id=$1 AND pr_binding_id IS NULL FOR UPDATE`, pipeline.PipelineID).Scan(&stored.PipelineID, &stored.TeamID, &stored.WorkflowDefinitionID, &stored.WorkflowName, &stored.WorkflowVersion, &stored.PipelineConfigVersion, &stored.ConfigHash, &storedDeclarations, &stored.State)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(storedDeclarations, &stored.SourceDeclarations); err != nil {
			return err
		}
		if !sameSourcePipeline(stored, pipeline) {
			return fmt.Errorf("db: workflow resource source pipeline ownership conflict")
		}
	}
	return tx.Commit()
}

// SaveAndActivateForPromotion creates the ordinary paused selection pipeline
// and registers it as active inside the caller's live-revision transaction.
// A repeated promotion of the same definition is accepted only when every
// persisted authority, including the frozen source declarations, is exact.
func (factory *workflowResourceSourcePipelinesFactory) SaveAndActivateForPromotion(
	ctx context.Context,
	tx Tx,
	trustedTeamID int,
	definition workflow.Definition,
	rendered workflow.RenderedResourceSourcePipeline,
) (WorkflowResourceSourcePipeline, error) {
	if ctx == nil || tx == nil || trustedTeamID <= 0 {
		return WorkflowResourceSourcePipeline{}, fmt.Errorf("db: source pipeline promotion requires context, transaction, and trusted team")
	}
	declarations, err := sourceDeclarationsForDefinition(definition)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	if err := validateRenderedResourceSourcePromotion(definition, trustedTeamID, rendered); err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	if err := lockWorkflowResourceSourcePipeline(ctx, tx, trustedTeamID, definition.Name); err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}

	registered, found, err := findWorkflowResourceSourcePipelineByDefinition(ctx, tx, trustedTeamID, definition.ID, true)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	if found {
		if registered.State != AgentWorkflowResourceSourcePipelineActive ||
			registered.WorkflowName != definition.Name ||
			registered.WorkflowVersion != definition.Version ||
			registered.ConfigHash != rendered.ConfigHash ||
			!reflect.DeepEqual(registered.SourceDeclarations, declarations) {
			return WorkflowResourceSourcePipeline{}, fmt.Errorf("db: registered source revision cannot be reactivated")
		}
		if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, registered); err != nil {
			return WorkflowResourceSourcePipeline{}, err
		}
		return registered, nil
	}

	previous, hadPrevious, err := findActiveWorkflowResourceSourcePipeline(ctx, tx, trustedTeamID, definition.Name, true)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	nullID := sql.NullInt64{Valid: false}
	pipelineID, created, err := savePipeline(
		tx,
		atc.PipelineRef{Name: rendered.PipelineName},
		rendered.Config,
		ConfigVersion(0),
		true,
		trustedTeamID,
		nullID,
		nullID,
	)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	if !created {
		return WorkflowResourceSourcePipeline{}, fmt.Errorf("db: generated source pipeline name already exists")
	}
	var (
		pipelineTeamID int
		configVersion  int
		pipelineName   string
		template       bool
		paused         bool
		archived       bool
	)
	err = tx.QueryRowContext(ctx, `
		SELECT team_id, version, name, template, paused, archived
		FROM pipelines WHERE id = $1 FOR UPDATE
	`, pipelineID).Scan(&pipelineTeamID, &configVersion, &pipelineName, &template, &paused, &archived)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	if pipelineTeamID != trustedTeamID || pipelineName != rendered.PipelineName || configVersion <= 0 || template || !paused || archived {
		return WorkflowResourceSourcePipeline{}, fmt.Errorf("db: saved source pipeline identity drifted")
	}
	if hadPrevious {
		result, err := tx.ExecContext(ctx, `
			UPDATE agent_workflow_resource_source_pipelines
			SET state='draining', updated_at=now()
			WHERE team_id=$1 AND pipeline_id=$2 AND state='active' AND pr_binding_id IS NULL
		`, trustedTeamID, previous.PipelineID)
		if err != nil {
			return WorkflowResourceSourcePipeline{}, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return WorkflowResourceSourcePipeline{}, err
		}
		if affected != 1 {
			return WorkflowResourceSourcePipeline{}, fmt.Errorf("db: active source revision changed during promotion")
		}
	}
	encodedDeclarations, err := json.Marshal(declarations)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_resource_source_pipelines
			(pipeline_id,team_id,workflow_definition_id,workflow_name,workflow_version,pipeline_config_version,config_hash,source_declarations,state)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'active')
	`, pipelineID, trustedTeamID, definition.ID, definition.Name, definition.Version, configVersion, rendered.ConfigHash, encodedDeclarations)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	stored, found, err := findWorkflowResourceSourcePipeline(ctx, tx, trustedTeamID, pipelineID, true)
	if err != nil {
		return WorkflowResourceSourcePipeline{}, err
	}
	if !found || !sameSourcePipeline(stored, WorkflowResourceSourcePipeline{
		PipelineID: pipelineID, TeamID: trustedTeamID, WorkflowDefinitionID: definition.ID,
		WorkflowName: definition.Name, WorkflowVersion: definition.Version,
		PipelineConfigVersion: configVersion, ConfigHash: rendered.ConfigHash,
		SourceDeclarations: declarations, State: AgentWorkflowResourceSourcePipelineActive,
	}) {
		return WorkflowResourceSourcePipeline{}, fmt.Errorf("db: persisted source pipeline registration drifted")
	}
	return stored, nil
}

func (factory *workflowResourceSourcePipelinesFactory) DrainActiveForPromotion(
	ctx context.Context,
	tx Tx,
	trustedTeamID int,
	workflowName string,
) (bool, error) {
	if ctx == nil || tx == nil || trustedTeamID <= 0 || strings.TrimSpace(workflowName) == "" {
		return false, fmt.Errorf("db: source pipeline drain requires context, transaction, team, and workflow")
	}
	if err := lockWorkflowResourceSourcePipeline(ctx, tx, trustedTeamID, workflowName); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_resource_source_pipelines
		SET state='draining', updated_at=now()
		WHERE team_id=$1 AND workflow_name=$2 AND state='active' AND pr_binding_id IS NULL
	`, trustedTeamID, workflowName)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func sourceDeclarationsForDefinition(definition workflow.Definition) ([]ResourceSourceDeclaration, error) {
	if definition.Compiled.Function == nil {
		return nil, fmt.Errorf("db: source pipeline requires a compiled function")
	}
	declarations := make([]ResourceSourceDeclaration, len(definition.Compiled.Function.ResourceSources))
	for index, source := range definition.Compiled.Function.ResourceSources {
		declarations[index] = ResourceSourceDeclaration{
			SourceName: source.Name, ResourceName: source.Resource, SnapshotType: source.Type,
		}
	}
	if err := validateSourceDeclarations(declarations); err != nil {
		return nil, err
	}
	return declarations, nil
}

func validateRenderedResourceSourcePromotion(
	definition workflow.Definition,
	trustedTeamID int,
	rendered workflow.RenderedResourceSourcePipeline,
) error {
	expected, hasSources, err := (DefaultWorkflowResourceSourcePipelineRenderer{}).RenderResourceSourcePipelineForPromotion(definition, trustedTeamID)
	if err != nil {
		return err
	}
	if !hasSources {
		return fmt.Errorf("db: source pipeline promotion requires resource sources")
	}
	canonical, err := rendered.Config.CanonicalJSON()
	if err != nil {
		return err
	}
	if rendered.Config.Template || rendered.PipelineName != expected.PipelineName || rendered.ConfigHash != expected.ConfigHash ||
		!bytes.Equal(rendered.CanonicalJSON, canonical) || !bytes.Equal(rendered.CanonicalJSON, expected.CanonicalJSON) {
		return fmt.Errorf("db: rendered source pipeline is not the exact compiled target")
	}
	return nil
}

func lockWorkflowResourceSourcePipeline(ctx context.Context, tx Tx, teamID int, workflowName string) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('agent_workflow_resource_source_pipelines:' || $1 || ':' || $2))`, strconv.Itoa(teamID), workflowName)
	return err
}

func (factory *workflowResourceSourcePipelinesFactory) Drain(ctx context.Context, teamID, pipelineID int) error {
	if ctx == nil || teamID <= 0 || pipelineID <= 0 {
		return fmt.Errorf("db: source pipeline drain requires context and positive identities")
	}
	result, err := factory.conn.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='draining', updated_at=now() WHERE team_id=$1 AND pipeline_id=$2 AND state='active' AND pr_binding_id IS NULL`, teamID, pipelineID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("db: source pipeline is not active and owned by this team")
	}
	return nil
}

func (factory *workflowResourceSourcePipelinesFactory) Archive(ctx context.Context, teamID, pipelineID int) error {
	if ctx == nil || teamID <= 0 || pipelineID <= 0 {
		return fmt.Errorf("db: source pipeline archive requires context and positive identities")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND pipeline_id=$2 AND pr_binding_id IS NULL FOR UPDATE`, teamID, pipelineID).Scan(&state); err != nil {
		return err
	}
	if state != "draining" {
		return fmt.Errorf("db: source pipeline must be draining before archive")
	}
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agent_workflow_resource_source_admissions WHERE team_id=$1 AND source_pipeline_id=$2 AND status IN ('selecting','capturing'))`, teamID, pipelineID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("db: source pipeline has in-flight admissions")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='archived', updated_at=now() WHERE team_id=$1 AND pipeline_id=$2 AND state='draining' AND pr_binding_id IS NULL`, teamID, pipelineID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("db: source pipeline archive changed concurrently")
	}
	return tx.Commit()
}

func (factory *workflowResourceSourcePipelinesFactory) Find(
	ctx context.Context,
	teamID, pipelineID int,
) (WorkflowResourceSourcePipeline, bool, error) {
	if ctx == nil || teamID <= 0 || pipelineID <= 0 {
		return WorkflowResourceSourcePipeline{}, false, fmt.Errorf("db: source pipeline lookup requires context, team, and pipeline")
	}
	return findWorkflowResourceSourcePipeline(ctx, factory.conn, teamID, pipelineID, false)
}

func (factory *workflowResourceSourcePipelinesFactory) FindActive(
	ctx context.Context,
	teamID int,
	workflowName string,
) (WorkflowResourceSourcePipeline, bool, error) {
	if ctx == nil || teamID <= 0 || strings.TrimSpace(workflowName) == "" {
		return WorkflowResourceSourcePipeline{}, false, fmt.Errorf("db: active source pipeline lookup requires context, team, and workflow")
	}
	return findActiveWorkflowResourceSourcePipeline(ctx, factory.conn, teamID, workflowName, false)
}

func (factory *workflowResourceSourcePipelinesFactory) ResourceSourcePipelineLifecycle(
	ctx context.Context,
	trustedTeamID int,
) ([]AgentWorkflowResourceSourcePipelineLifecycle, error) {
	if ctx == nil || trustedTeamID <= 0 {
		return nil, fmt.Errorf("db: source pipeline lifecycle lookup requires context and trusted team")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT source.pipeline_id,source.team_id,source.pr_binding_id,
		       source.workflow_definition_id,
		       source.workflow_name,source.workflow_version,source.pipeline_config_version,
		       source.config_hash,source.source_declarations,source.state,
		       pipeline.paused,pipeline.archived,
		       (SELECT count(*) FROM builds build JOIN jobs job ON job.id=build.job_id
		        WHERE job.pipeline_id=source.pipeline_id AND build.status IN ('pending','started')),
		       (SELECT count(*) FROM agent_workflow_resource_source_admissions admission
		        WHERE admission.team_id=source.team_id AND admission.source_pipeline_id=source.pipeline_id
		          AND admission.status IN ('selecting','capturing'))
		FROM agent_workflow_resource_source_pipelines source
		JOIN pipelines pipeline ON pipeline.id=source.pipeline_id
		WHERE source.team_id=$1
		ORDER BY source.pipeline_id
	`, trustedTeamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []AgentWorkflowResourceSourcePipelineLifecycle{}
	for rows.Next() {
		var candidate AgentWorkflowResourceSourcePipelineLifecycle
		var bindingID sql.NullInt64
		var declarations []byte
		if err := rows.Scan(
			&candidate.PipelineID, &candidate.TeamID, &bindingID,
			&candidate.WorkflowDefinitionID,
			&candidate.WorkflowName, &candidate.WorkflowVersion, &candidate.PipelineConfigVersion,
			&candidate.ConfigHash, &declarations, &candidate.State,
			&candidate.Paused, &candidate.Archived, &candidate.InFlightBuilds, &candidate.NonterminalAdmissions,
		); err != nil {
			return nil, err
		}
		if bindingID.Valid {
			value := bindingID.Int64
			candidate.PRBindingID = &value
		}
		if err := json.Unmarshal(declarations, &candidate.SourceDeclarations); err != nil {
			return nil, fmt.Errorf("db: decode source pipeline lifecycle declarations: %w", err)
		}
		if err := validateWorkflowResourceSourcePipeline(candidate.AgentWorkflowResourceSourcePipeline); err != nil {
			return nil, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.State != AgentWorkflowResourceSourcePipelineArchived {
			if err := validateWorkflowResourceSourcePipelineAuthority(ctx, factory.conn, candidate.AgentWorkflowResourceSourcePipeline); err != nil {
				return nil, err
			}
		}
	}
	return candidates, nil
}

func (factory *workflowResourceSourcePipelinesFactory) UnpauseActiveResourceSourcePipeline(
	ctx context.Context,
	trustedTeamID int,
	expected WorkflowResourceSourcePipeline,
) (bool, error) {
	if ctx == nil || trustedTeamID <= 0 {
		return false, fmt.Errorf("db: source pipeline unpause requires context and trusted team")
	}
	if expected.PRBindingID != nil {
		return false, fmt.Errorf(
			"%w: binding source pipeline requires binding-scoped unpause",
			ErrAgentWorkflowResourceSourceConflict,
		)
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	stored, found, err := findExpectedWorkflowResourceSourcePipeline(
		ctx, tx, trustedTeamID, expected, true,
	)
	if err != nil {
		return false, err
	}
	if !found || expected.State != AgentWorkflowResourceSourcePipelineActive || !sameSourcePipeline(stored, expected) {
		return false, fmt.Errorf("db: active source pipeline changed")
	}
	if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, stored); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE pipelines SET paused=false, paused_by=NULL, paused_at=NULL
		WHERE id=$1 AND team_id=$2 AND version=$3 AND paused AND NOT archived AND NOT template
	`, stored.PipelineID, trustedTeamID, stored.PipelineConfigVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		if err := requestScheduleForJobsInPipeline(tx, stored.PipelineID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (factory *workflowResourceSourcePipelinesFactory) PauseDrainedResourceSourcePipeline(
	ctx context.Context,
	trustedTeamID int,
	expected WorkflowResourceSourcePipeline,
) (bool, error) {
	if ctx == nil || trustedTeamID <= 0 {
		return false, fmt.Errorf("db: source pipeline pause requires context and trusted team")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	stored, found, err := findExpectedWorkflowResourceSourcePipeline(
		ctx, tx, trustedTeamID, expected, true,
	)
	if err != nil {
		return false, err
	}
	if expected.State != AgentWorkflowResourceSourcePipelineDraining ||
		!found ||
		(!sameSourcePipeline(stored, expected) &&
			!(stored.State == AgentWorkflowResourceSourcePipelineArchived &&
				sameSourcePipelineIdentity(stored, expected))) {
		return false, fmt.Errorf("db: draining source pipeline changed")
	}
	if stored.State == AgentWorkflowResourceSourcePipelineArchived {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, stored); err != nil {
		return false, err
	}
	inFlight, err := workflowResourceSourcePipelineInFlightBuilds(ctx, tx, stored.PipelineID)
	if err != nil {
		return false, err
	}
	if inFlight != 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE pipelines SET paused=true,paused_by='agent-workflow-resource-source-lifecycle',paused_at=now()
		WHERE id=$1 AND team_id=$2 AND version=$3 AND NOT paused AND NOT archived AND NOT template
	`, stored.PipelineID, trustedTeamID, stored.PipelineConfigVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return affected == 1, nil
}

func (factory *workflowResourceSourcePipelinesFactory) ArchiveDrainedResourceSourcePipeline(
	ctx context.Context,
	trustedTeamID int,
	expected WorkflowResourceSourcePipeline,
) (bool, error) {
	if ctx == nil || trustedTeamID <= 0 {
		return false, fmt.Errorf("db: source pipeline archive requires context and trusted team")
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	stored, found, err := findExpectedWorkflowResourceSourcePipeline(
		ctx, tx, trustedTeamID, expected, true,
	)
	if err != nil {
		return false, err
	}
	if expected.State != AgentWorkflowResourceSourcePipelineDraining ||
		!found ||
		(!sameSourcePipeline(stored, expected) &&
			!(stored.State == AgentWorkflowResourceSourcePipelineArchived &&
				sameSourcePipelineIdentity(stored, expected))) {
		return false, fmt.Errorf("db: draining source pipeline changed")
	}
	if stored.State == AgentWorkflowResourceSourcePipelineArchived {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, stored); err != nil {
		return false, err
	}
	inFlight, err := workflowResourceSourcePipelineInFlightBuilds(ctx, tx, stored.PipelineID)
	if err != nil {
		return false, err
	}
	if inFlight != 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	var nonterminal int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_workflow_resource_source_admissions WHERE team_id=$1 AND source_pipeline_id=$2 AND status IN ('selecting','capturing')`, trustedTeamID, stored.PipelineID).Scan(&nonterminal); err != nil {
		return false, err
	}
	if nonterminal != 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE pipelines SET archived=true,last_updated=now(),paused=true,
		paused_by='agent-workflow-resource-source-lifecycle',paused_at=COALESCE(paused_at,now()),version=0
		WHERE id=$1 AND team_id=$2 AND version=$3 AND paused AND NOT archived AND NOT template
	`, stored.PipelineID, trustedTeamID, stored.PipelineConfigVersion)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	for _, table := range pipelineObjectTables {
		if err := clearConfigForPipelineObject(tx, stored.PipelineID, table); err != nil {
			return false, err
		}
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE agent_workflow_resource_source_pipelines
		SET state='archived',updated_at=now()
		WHERE team_id=$1 AND pipeline_id=$2 AND state='draining'
		  AND pr_binding_id IS NOT DISTINCT FROM $3::bigint
	`, trustedTeamID, stored.PipelineID, nullablePRBindingID(stored.PRBindingID))
	if err != nil {
		return false, err
	}
	registryAffected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if registryAffected != 1 {
		return false, fmt.Errorf("db: draining source registry changed during archive")
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	factory.conn.Bus().Notify(atc.ComponentCollectorPipelines)
	factory.conn.Bus().Notify(atc.ComponentCollectorTaskCaches)
	return true, nil
}

func workflowResourceSourcePipelineInFlightBuilds(ctx context.Context, queryer snapshotQueryer, pipelineID int) (int, error) {
	var count int
	err := queryer.QueryRowContext(ctx, `SELECT count(*) FROM builds build JOIN jobs job ON job.id=build.job_id WHERE job.pipeline_id=$1 AND build.status IN ('pending','started')`, pipelineID).Scan(&count)
	return count, err
}

type CreateWorkflowResourceSourceAdmissionRequest struct {
	TeamID               int
	WorkflowDefinitionID int
	SourcePipelineID     int
	SourceConfigHash     string
	IdempotencyKey       string
	Mode                 string
	SelectingBuildID     int64
}

// WorkflowResourceSourceAdmissionsFactory persists only versions already
// selected by a successful admit build; it has no latest-selection API.
type WorkflowResourceSourceAdmissionsFactory interface {
	CreateCaptured(context.Context, CreateWorkflowResourceSourceAdmissionRequest) (int64, error)
}
type workflowResourceSourceAdmissionsFactory struct{ conn DbConn }

func NewWorkflowResourceSourceAdmissionsFactory(conn DbConn) WorkflowResourceSourceAdmissionsFactory {
	return &workflowResourceSourceAdmissionsFactory{conn: conn}
}

func (factory *workflowResourceSourceAdmissionsFactory) CreateCaptured(ctx context.Context, request CreateWorkflowResourceSourceAdmissionRequest) (int64, error) {
	if err := validateAdmissionRequest(request); err != nil {
		return 0, err
	}
	tx, err := factory.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)
	var configVersion int
	var declarationJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT pipeline_config_version, source_declarations FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND pipeline_id=$2 AND workflow_definition_id=$3 AND config_hash=$4 AND state='active' AND pr_binding_id IS NULL FOR UPDATE`, request.TeamID, request.SourcePipelineID, request.WorkflowDefinitionID, request.SourceConfigHash).Scan(&configVersion, &declarationJSON)
	if err != nil {
		return 0, fmt.Errorf("db: source admission pipeline authority: %w", err)
	}
	var declarations []ResourceSourceDeclaration
	if err := json.Unmarshal(declarationJSON, &declarations); err != nil {
		return 0, fmt.Errorf("db: decode frozen source declarations: %w", err)
	}
	if err := validateSourceDeclarations(declarations); err != nil {
		return 0, err
	}
	var selectingBuildValid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM builds b JOIN jobs j ON j.id=b.job_id WHERE b.id=$1 AND b.team_id=$2 AND b.pipeline_id=$3 AND b.pipeline_config_version=$4 AND j.name='admit' AND b.status='succeeded' AND b.completed AND NOT b.aborted)`, request.SelectingBuildID, request.TeamID, request.SourcePipelineID, configVersion).Scan(&selectingBuildValid); err != nil {
		return 0, err
	}
	if !selectingBuildValid {
		return 0, fmt.Errorf("db: source admission selecting build is not an exact successful admit build")
	}
	bindings, err := deriveSourceBindings(ctx, tx, request, configVersion, declarations)
	if err != nil {
		return 0, err
	}
	var id int64
	var existing CreateWorkflowResourceSourceAdmissionRequest
	err = tx.QueryRowContext(ctx, `INSERT INTO agent_workflow_resource_source_admissions (team_id,workflow_definition_id,source_pipeline_id,source_config_hash,idempotency_key,mode,selecting_build_id,status) VALUES ($1,$2,$3,$4,$5,$6,$7,'capturing') ON CONFLICT (team_id,idempotency_key) DO UPDATE SET id=agent_workflow_resource_source_admissions.id RETURNING id,team_id,workflow_definition_id,source_pipeline_id,source_config_hash,idempotency_key,mode,selecting_build_id`, request.TeamID, request.WorkflowDefinitionID, request.SourcePipelineID, request.SourceConfigHash, request.IdempotencyKey, request.Mode, request.SelectingBuildID).Scan(&id, &existing.TeamID, &existing.WorkflowDefinitionID, &existing.SourcePipelineID, &existing.SourceConfigHash, &existing.IdempotencyKey, &existing.Mode, &existing.SelectingBuildID)
	if err != nil {
		return 0, err
	}
	if !sameAdmissionRequest(existing, request) {
		return 0, fmt.Errorf("db: workflow resource source admission idempotency conflict")
	}
	for _, binding := range bindings {
		version, err := json.Marshal(binding.Version)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_workflow_resource_source_bindings (admission_id,source_name,resource_name,selecting_build_id,source_pipeline_id,pipeline_config_version,resource_id,resource_config_version_id,resource_version_id,version_digest,version,snapshot_type_name,snapshot_type_version,capture_operation_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,$10,$11,$12,$13) ON CONFLICT (admission_id,source_name) DO NOTHING`, id, binding.SourceName, binding.ResourceName, request.SelectingBuildID, request.SourcePipelineID, configVersion, binding.ResourceID, binding.ResourceConfigVersionID, binding.VersionDigest, version, snapshotTypeName(binding.SnapshotType), snapshotTypeVersion(binding.SnapshotType), binding.CaptureOperationKey)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

type derivedResourceSourceBinding struct {
	SourceName              string
	ResourceName            string
	ResourceID              int
	ResourceConfigVersionID int64
	VersionDigest           string
	Version                 atc.Version
	SnapshotType            snapshot.TypeRef
	CaptureOperationKey     string
}

func deriveSourceBindings(ctx context.Context, tx Tx, request CreateWorkflowResourceSourceAdmissionRequest, configVersion int, declarations []ResourceSourceDeclaration) ([]derivedResourceSourceBinding, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT input.name, resource.name, resource.id, version.id, input.version_digest, version.version
		FROM build_resource_config_version_inputs input
		JOIN resources resource ON resource.id = input.resource_id
		JOIN resource_config_versions version
		  ON version.resource_config_scope_id = resource.resource_config_scope_id
		 AND input.version_digest IN (version.version_md5, version.version_sha256)
		WHERE input.build_id = $1 AND resource.pipeline_id = $2
		ORDER BY input.name, version.id
		FOR SHARE OF input, resource, version`, request.SelectingBuildID, request.SourcePipelineID)
	if err != nil {
		return nil, err
	}
	declarationsBySource := make(map[string]ResourceSourceDeclaration, len(declarations))
	for _, declaration := range declarations {
		declarationsBySource[declaration.SourceName] = declaration
	}
	bindings := make([]derivedResourceSourceBinding, 0, len(declarations))
	seen := make(map[string]struct{}, len(declarations))
	for rows.Next() {
		var binding derivedResourceSourceBinding
		var versionJSON []byte
		if err := rows.Scan(&binding.SourceName, &binding.ResourceName, &binding.ResourceID, &binding.ResourceConfigVersionID, &binding.VersionDigest, &versionJSON); err != nil {
			rows.Close()
			return nil, err
		}
		declaration, found := declarationsBySource[binding.SourceName]
		if !found || declaration.ResourceName != binding.ResourceName {
			rows.Close()
			return nil, fmt.Errorf("db: selecting build input %q does not match frozen source declaration", binding.SourceName)
		}
		if _, duplicate := seen[binding.SourceName]; duplicate {
			rows.Close()
			return nil, fmt.Errorf("db: selecting build resolves source %q more than once", binding.SourceName)
		}
		if err := json.Unmarshal(versionJSON, &binding.Version); err != nil {
			rows.Close()
			return nil, fmt.Errorf("db: decode selecting build source version: %w", err)
		}
		binding.SnapshotType = declaration.SnapshotType
		binding.CaptureOperationKey = derivedCaptureOperationKey(request, configVersion, binding)
		seen[binding.SourceName] = struct{}{}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(bindings) != len(declarations) {
		return nil, fmt.Errorf("db: selecting build does not contain every frozen source declaration")
	}
	return bindings, nil
}

func derivedCaptureOperationKey(request CreateWorkflowResourceSourceAdmissionRequest, configVersion int, binding derivedResourceSourceBinding) string {
	key, _ := WorkflowResourceSourceCaptureOperationKey(
		request.TeamID,
		request.WorkflowDefinitionID,
		request.SourcePipelineID,
		configVersion,
		binding.SourceName,
		binding.ResourceName,
		binding.VersionDigest,
		binding.SnapshotType,
	)
	return key
}

// WorkflowResourceSourceCaptureOperationKey derives the immutable capture
// identity from the same durable selecting-build evidence used by Task 12.
// Runtime capture reuses this function so it cannot revive the historical
// admission-ID string key or substitute another source selection.
func WorkflowResourceSourceCaptureOperationKey(
	teamID, definitionID, pipelineID, pipelineConfigVersion int,
	sourceName, resourceName, versionDigest string,
	snapshotType snapshot.TypeRef,
) (string, error) {
	if teamID <= 0 || definitionID <= 0 || pipelineID <= 0 || pipelineConfigVersion <= 0 ||
		strings.TrimSpace(sourceName) == "" || strings.TrimSpace(resourceName) == "" ||
		!validResourceSourceVersionDigest(versionDigest) || snapshotType.Validate() != nil {
		return "", fmt.Errorf("db: invalid workflow resource source capture identity")
	}
	payload, err := atc.CanonicalJSON(struct {
		TeamID                int              `json:"team_id"`
		DefinitionID          int              `json:"definition_id"`
		PipelineID            int              `json:"pipeline_id"`
		PipelineConfigVersion int              `json:"pipeline_config_version"`
		Source                string           `json:"source"`
		Resource              string           `json:"resource"`
		VersionDigest         string           `json:"version_digest"`
		SnapshotType          snapshot.TypeRef `json:"snapshot_type"`
	}{teamID, definitionID, pipelineID, pipelineConfigVersion, sourceName, resourceName, versionDigest, snapshotType})
	if err != nil {
		return "", fmt.Errorf("db: canonicalize workflow resource source capture identity: %w", err)
	}
	sum := sha256.Sum256(append([]byte("workflow-resource-source-capture/v1\x00"), payload...))
	return fmt.Sprintf("%x", sum[:]), nil
}

func validResourceSourceVersionDigest(value string) bool {
	return (len(value) == 32 || len(value) == 64) && regexp.MustCompile(`^[0-9a-f]+$`).MatchString(value)
}

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateWorkflowResourceSourcePipeline(p WorkflowResourceSourcePipeline) error {
	if p.TeamID <= 0 || p.PipelineID <= 0 || p.WorkflowDefinitionID <= 0 || p.WorkflowVersion <= 0 || p.PipelineConfigVersion <= 0 || (p.PRBindingID != nil && *p.PRBindingID <= 0) || strings.TrimSpace(p.WorkflowName) == "" || !lowerHex64.MatchString(p.ConfigHash) || (p.State != "" && p.State.Validate() != nil) || validateSourceDeclarations(p.SourceDeclarations) != nil {
		return fmt.Errorf("db: invalid workflow resource source pipeline")
	}
	return nil
}
func validateAdmissionRequest(r CreateWorkflowResourceSourceAdmissionRequest) error {
	if r.TeamID <= 0 || r.WorkflowDefinitionID <= 0 || r.SourcePipelineID <= 0 || r.SelectingBuildID <= 0 || strings.TrimSpace(r.IdempotencyKey) == "" || !lowerHex64.MatchString(r.SourceConfigHash) || (r.Mode != "manual" && r.Mode != "automatic") {
		return fmt.Errorf("db: invalid workflow resource source admission")
	}
	return nil
}

func validateSourceDeclarations(declarations []ResourceSourceDeclaration) error {
	if len(declarations) == 0 {
		return fmt.Errorf("db: source declarations are required")
	}
	seen := map[string]struct{}{}
	for _, declaration := range declarations {
		if strings.TrimSpace(declaration.SourceName) == "" || strings.TrimSpace(declaration.ResourceName) == "" || declaration.SnapshotType.Validate() != nil {
			return fmt.Errorf("db: invalid source declaration")
		}
		if _, found := seen[declaration.SourceName]; found {
			return fmt.Errorf("db: duplicate source declaration %q", declaration.SourceName)
		}
		seen[declaration.SourceName] = struct{}{}
	}
	return nil
}

func sameSourcePipeline(left, right WorkflowResourceSourcePipeline) bool {
	return left.State == right.State &&
		sameSourcePipelineIdentity(left, right)
}

func sameSourcePipelineIdentity(
	left,
	right WorkflowResourceSourcePipeline,
) bool {
	return left.PipelineID == right.PipelineID &&
		left.TeamID == right.TeamID &&
		sameOptionalPRBindingID(left.PRBindingID, right.PRBindingID) &&
		left.WorkflowDefinitionID == right.WorkflowDefinitionID &&
		left.WorkflowName == right.WorkflowName &&
		left.WorkflowVersion == right.WorkflowVersion &&
		left.PipelineConfigVersion == right.PipelineConfigVersion &&
		left.ConfigHash == right.ConfigHash &&
		reflect.DeepEqual(
			left.SourceDeclarations, right.SourceDeclarations,
		)
}

func sameOptionalPRBindingID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameAdmissionRequest(left, right CreateWorkflowResourceSourceAdmissionRequest) bool {
	return left.TeamID == right.TeamID && left.WorkflowDefinitionID == right.WorkflowDefinitionID && left.SourcePipelineID == right.SourcePipelineID && left.SourceConfigHash == right.SourceConfigHash && left.IdempotencyKey == right.IdempotencyKey && left.Mode == right.Mode && left.SelectingBuildID == right.SelectingBuildID
}

func findWorkflowResourceSourcePipelineByDefinition(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID, definitionID int,
	lock bool,
) (WorkflowResourceSourcePipeline, bool, error) {
	query := `SELECT pipeline_id,team_id,workflow_definition_id,workflow_name,workflow_version,pipeline_config_version,config_hash,source_declarations,state FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND workflow_definition_id=$2 AND pr_binding_id IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWorkflowResourceSourcePipeline(queryer.QueryRowContext(ctx, query, teamID, definitionID))
}

func findActiveWorkflowResourceSourcePipeline(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	workflowName string,
	lock bool,
) (WorkflowResourceSourcePipeline, bool, error) {
	query := `SELECT pipeline_id,team_id,workflow_definition_id,workflow_name,workflow_version,pipeline_config_version,config_hash,source_declarations,state FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND workflow_name=$2 AND state='active' AND pr_binding_id IS NULL`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWorkflowResourceSourcePipeline(queryer.QueryRowContext(ctx, query, teamID, workflowName))
}

func findExpectedWorkflowResourceSourcePipeline(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	expected WorkflowResourceSourcePipeline,
	lock bool,
) (WorkflowResourceSourcePipeline, bool, error) {
	return findWorkflowResourceSourcePipeline(
		ctx, queryer, teamID, expected.PipelineID, lock,
	)
}

func scanWorkflowResourceSourcePipeline(row interface{ Scan(...any) error }) (WorkflowResourceSourcePipeline, bool, error) {
	var pipeline WorkflowResourceSourcePipeline
	var declarations []byte
	err := row.Scan(
		&pipeline.PipelineID, &pipeline.TeamID, &pipeline.WorkflowDefinitionID,
		&pipeline.WorkflowName, &pipeline.WorkflowVersion, &pipeline.PipelineConfigVersion,
		&pipeline.ConfigHash, &declarations, &pipeline.State,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowResourceSourcePipeline{}, false, nil
	}
	if err != nil {
		return WorkflowResourceSourcePipeline{}, false, err
	}
	if err := json.Unmarshal(declarations, &pipeline.SourceDeclarations); err != nil {
		return WorkflowResourceSourcePipeline{}, false, fmt.Errorf("db: decode source pipeline declarations: %w", err)
	}
	if err := validateWorkflowResourceSourcePipeline(pipeline); err != nil {
		return WorkflowResourceSourcePipeline{}, false, err
	}
	return pipeline, true, nil
}

func nullablePRBindingID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func snapshotTypeName(t snapshot.TypeRef) string { return strings.Split(string(t), "/v")[0] }
func snapshotTypeVersion(t snapshot.TypeRef) int {
	index := strings.LastIndex(string(t), "/v")
	if index < 0 {
		return 0
	}
	version, _ := strconv.Atoi(string(t)[index+2:])
	return version
}
