package db

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

// WorkflowResourceSourcePipeline is the durable ownership record for one
// ordinary source-selection pipeline. Its state is deliberately independent
// from pipeline.archived so only this factory can transition its lifecycle.
type WorkflowResourceSourcePipeline struct {
	PipelineID            int
	TeamID                int
	WorkflowDefinitionID  int
	WorkflowName          string
	WorkflowVersion       int
	PipelineConfigVersion int
	ConfigHash            string
	State                 string
}

type WorkflowResourceSourcePipelinesFactory interface {
	Activate(context.Context, WorkflowResourceSourcePipeline) error
	Drain(context.Context, int, int) error
	Archive(context.Context, int, int) error
}

type workflowResourceSourcePipelinesFactory struct{ conn DbConn }

func NewWorkflowResourceSourcePipelinesFactory(conn DbConn) WorkflowResourceSourcePipelinesFactory {
	return &workflowResourceSourcePipelinesFactory{conn: conn}
}

func (factory *workflowResourceSourcePipelinesFactory) Activate(ctx context.Context, pipeline WorkflowResourceSourcePipeline) error {
	if err := validateWorkflowResourceSourcePipeline(pipeline); err != nil {
		return err
	}
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
	if _, err := tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='draining', updated_at=now() WHERE team_id=$1 AND workflow_name=$2 AND state='active' AND pipeline_id<>$3`, pipeline.TeamID, pipeline.WorkflowName, pipeline.PipelineID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO agent_workflow_resource_source_pipelines (pipeline_id, team_id, workflow_definition_id, workflow_name, workflow_version, pipeline_config_version, config_hash, state) VALUES ($1,$2,$3,$4,$5,$6,$7,'active') ON CONFLICT (pipeline_id) DO NOTHING`, pipeline.PipelineID, pipeline.TeamID, pipeline.WorkflowDefinitionID, pipeline.WorkflowName, pipeline.WorkflowVersion, pipeline.PipelineConfigVersion, pipeline.ConfigHash)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var stored WorkflowResourceSourcePipeline
		err = tx.QueryRowContext(ctx, `SELECT pipeline_id, team_id, workflow_definition_id, workflow_name, workflow_version, pipeline_config_version, config_hash, state FROM agent_workflow_resource_source_pipelines WHERE pipeline_id=$1 FOR UPDATE`, pipeline.PipelineID).Scan(&stored.PipelineID, &stored.TeamID, &stored.WorkflowDefinitionID, &stored.WorkflowName, &stored.WorkflowVersion, &stored.PipelineConfigVersion, &stored.ConfigHash, &stored.State)
		if err != nil {
			return err
		}
		if stored != pipeline {
			return fmt.Errorf("db: workflow resource source pipeline ownership conflict")
		}
	}
	return tx.Commit()
}

func (factory *workflowResourceSourcePipelinesFactory) Drain(ctx context.Context, teamID, pipelineID int) error {
	if ctx == nil || teamID <= 0 || pipelineID <= 0 {
		return fmt.Errorf("db: source pipeline drain requires context and positive identities")
	}
	result, err := factory.conn.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='draining', updated_at=now() WHERE team_id=$1 AND pipeline_id=$2 AND state='active'`, teamID, pipelineID)
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
	var busy bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agent_workflow_resource_source_admissions WHERE team_id=$1 AND source_pipeline_id=$2 AND status IN ('selecting','capturing'))`, teamID, pipelineID).Scan(&busy); err != nil {
		return err
	}
	if busy {
		return fmt.Errorf("db: source pipeline has in-flight admissions")
	}
	result, err := tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='archived', updated_at=now() WHERE team_id=$1 AND pipeline_id=$2 AND state='draining'`, teamID, pipelineID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("db: source pipeline must be draining before archive")
	}
	return tx.Commit()
}

type ResourceSourceBinding struct {
	SourceName              string
	ResourceName            string
	ResourceID              int
	ResourceConfigVersionID int64
	ResourceVersionID       int64
	VersionDigest           string
	Version                 atc.Version
	SnapshotType            snapshot.TypeRef
	CaptureOperationKey     string
}

type CreateWorkflowResourceSourceAdmissionRequest struct {
	TeamID               int
	WorkflowDefinitionID int
	SourcePipelineID     int
	SourceConfigHash     string
	IdempotencyKey       string
	Mode                 string
	SelectingBuildID     int64
	Bindings             []ResourceSourceBinding
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
	err = tx.QueryRowContext(ctx, `SELECT pipeline_config_version FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND pipeline_id=$2 AND workflow_definition_id=$3 AND config_hash=$4 AND state='active' FOR SHARE`, request.TeamID, request.SourcePipelineID, request.WorkflowDefinitionID, request.SourceConfigHash).Scan(&configVersion)
	if err != nil {
		return 0, fmt.Errorf("db: source admission pipeline authority: %w", err)
	}
	var selectingBuildValid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM builds b JOIN jobs j ON j.id=b.job_id WHERE b.id=$1 AND b.team_id=$2 AND b.pipeline_id=$3 AND b.pipeline_config_version=$4 AND j.name='admit' AND b.status='succeeded' AND b.completed AND NOT b.aborted)`, request.SelectingBuildID, request.TeamID, request.SourcePipelineID, configVersion).Scan(&selectingBuildValid); err != nil {
		return 0, err
	}
	if !selectingBuildValid {
		return 0, fmt.Errorf("db: source admission selecting build is not an exact successful admit build")
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO agent_workflow_resource_source_admissions (team_id,workflow_definition_id,source_pipeline_id,source_config_hash,idempotency_key,mode,selecting_build_id,status) VALUES ($1,$2,$3,$4,$5,$6,$7,'capturing') ON CONFLICT (team_id,idempotency_key) DO UPDATE SET id=agent_workflow_resource_source_admissions.id RETURNING id`, request.TeamID, request.WorkflowDefinitionID, request.SourcePipelineID, request.SourceConfigHash, request.IdempotencyKey, request.Mode, request.SelectingBuildID).Scan(&id)
	if err != nil {
		return 0, err
	}
	for _, binding := range request.Bindings {
		version, err := json.Marshal(binding.Version)
		if err != nil {
			return 0, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO agent_workflow_resource_source_bindings (admission_id,source_name,resource_name,selecting_build_id,source_pipeline_id,pipeline_config_version,resource_id,resource_config_version_id,resource_version_id,version_digest,version,snapshot_type_name,snapshot_type_version,capture_operation_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (admission_id,source_name) DO NOTHING`, id, binding.SourceName, binding.ResourceName, request.SelectingBuildID, request.SourcePipelineID, configVersion, binding.ResourceID, binding.ResourceConfigVersionID, binding.ResourceVersionID, binding.VersionDigest, version, snapshotTypeName(binding.SnapshotType), snapshotTypeVersion(binding.SnapshotType), binding.CaptureOperationKey)
		if err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateWorkflowResourceSourcePipeline(p WorkflowResourceSourcePipeline) error {
	if p.TeamID <= 0 || p.PipelineID <= 0 || p.WorkflowDefinitionID <= 0 || p.WorkflowVersion <= 0 || p.PipelineConfigVersion <= 0 || strings.TrimSpace(p.WorkflowName) == "" || !lowerHex64.MatchString(p.ConfigHash) {
		return fmt.Errorf("db: invalid workflow resource source pipeline")
	}
	return nil
}
func validateAdmissionRequest(r CreateWorkflowResourceSourceAdmissionRequest) error {
	if r.TeamID <= 0 || r.WorkflowDefinitionID <= 0 || r.SourcePipelineID <= 0 || r.SelectingBuildID <= 0 || strings.TrimSpace(r.IdempotencyKey) == "" || !lowerHex64.MatchString(r.SourceConfigHash) || (r.Mode != "manual" && r.Mode != "automatic") || len(r.Bindings) == 0 {
		return fmt.Errorf("db: invalid workflow resource source admission")
	}
	seen := map[string]struct{}{}
	for _, b := range r.Bindings {
		if strings.TrimSpace(b.SourceName) == "" || strings.TrimSpace(b.ResourceName) == "" || b.ResourceID <= 0 || b.ResourceConfigVersionID <= 0 || b.ResourceVersionID != b.ResourceConfigVersionID || !regexp.MustCompile(`^[0-9a-f]{32}([0-9a-f]{32})?$`).MatchString(b.VersionDigest) || len(b.Version) == 0 || strings.TrimSpace(b.CaptureOperationKey) == "" || b.SnapshotType.Validate() != nil {
			return fmt.Errorf("db: invalid workflow resource source binding")
		}
		if _, ok := seen[b.SourceName]; ok {
			return fmt.Errorf("db: duplicate workflow resource source binding %q", b.SourceName)
		}
		seen[b.SourceName] = struct{}{}
	}
	return nil
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
