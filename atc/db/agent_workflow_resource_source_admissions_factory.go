package db

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"reflect"
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
	SourceDeclarations    []ResourceSourceDeclaration
	State                 string
}

// ResourceSourceDeclaration is frozen alongside the pipeline registration so
// the admission writer can prove a selecting build maps to its declared port.
type ResourceSourceDeclaration struct {
	SourceName   string           `json:"source_name"`
	ResourceName string           `json:"resource_name"`
	SnapshotType snapshot.TypeRef `json:"snapshot_type"`
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
	if _, err := tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='draining', updated_at=now() WHERE team_id=$1 AND workflow_name=$2 AND state='active' AND pipeline_id<>$3`, pipeline.TeamID, pipeline.WorkflowName, pipeline.PipelineID); err != nil {
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
		err = tx.QueryRowContext(ctx, `SELECT pipeline_id, team_id, workflow_definition_id, workflow_name, workflow_version, pipeline_config_version, config_hash, source_declarations, state FROM agent_workflow_resource_source_pipelines WHERE pipeline_id=$1 FOR UPDATE`, pipeline.PipelineID).Scan(&stored.PipelineID, &stored.TeamID, &stored.WorkflowDefinitionID, &stored.WorkflowName, &stored.WorkflowVersion, &stored.PipelineConfigVersion, &stored.ConfigHash, &storedDeclarations, &stored.State)
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
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND pipeline_id=$2 FOR UPDATE`, teamID, pipelineID).Scan(&state); err != nil {
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
	result, err := tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_pipelines SET state='archived', updated_at=now() WHERE team_id=$1 AND pipeline_id=$2 AND state='draining'`, teamID, pipelineID)
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
	err = tx.QueryRowContext(ctx, `SELECT pipeline_config_version, source_declarations FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND pipeline_id=$2 AND workflow_definition_id=$3 AND config_hash=$4 AND state='active' FOR UPDATE`, request.TeamID, request.SourcePipelineID, request.WorkflowDefinitionID, request.SourceConfigHash).Scan(&configVersion, &declarationJSON)
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
	payload, _ := atc.CanonicalJSON(struct {
		TeamID                int              `json:"team_id"`
		DefinitionID          int              `json:"definition_id"`
		PipelineID            int              `json:"pipeline_id"`
		PipelineConfigVersion int              `json:"pipeline_config_version"`
		Source                string           `json:"source"`
		Resource              string           `json:"resource"`
		VersionDigest         string           `json:"version_digest"`
		SnapshotType          snapshot.TypeRef `json:"snapshot_type"`
	}{request.TeamID, request.WorkflowDefinitionID, request.SourcePipelineID, configVersion, binding.SourceName, binding.ResourceName, binding.VersionDigest, binding.SnapshotType})
	sum := sha256.Sum256(append([]byte("workflow-resource-source-capture/v1\x00"), payload...))
	return fmt.Sprintf("%x", sum[:])
}

var lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validateWorkflowResourceSourcePipeline(p WorkflowResourceSourcePipeline) error {
	if p.TeamID <= 0 || p.PipelineID <= 0 || p.WorkflowDefinitionID <= 0 || p.WorkflowVersion <= 0 || p.PipelineConfigVersion <= 0 || strings.TrimSpace(p.WorkflowName) == "" || !lowerHex64.MatchString(p.ConfigHash) || (p.State != "" && p.State != "active") || validateSourceDeclarations(p.SourceDeclarations) != nil {
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
	return left.PipelineID == right.PipelineID && left.TeamID == right.TeamID && left.WorkflowDefinitionID == right.WorkflowDefinitionID && left.WorkflowName == right.WorkflowName && left.WorkflowVersion == right.WorkflowVersion && left.PipelineConfigVersion == right.PipelineConfigVersion && left.ConfigHash == right.ConfigHash && left.State == right.State && reflect.DeepEqual(left.SourceDeclarations, right.SourceDeclarations)
}

func sameAdmissionRequest(left, right CreateWorkflowResourceSourceAdmissionRequest) bool {
	return left.TeamID == right.TeamID && left.WorkflowDefinitionID == right.WorkflowDefinitionID && left.SourcePipelineID == right.SourcePipelineID && left.SourceConfigHash == right.SourceConfigHash && left.IdempotencyKey == right.IdempotencyKey && left.Mode == right.Mode && left.SelectingBuildID == right.SelectingBuildID
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
