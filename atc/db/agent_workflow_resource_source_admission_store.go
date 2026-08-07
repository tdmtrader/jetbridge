package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
)

const agentWorkflowResourceSourceAdmissionColumns = `
	id, team_id, workflow_definition_id, source_pipeline_id,
	source_config_hash, idempotency_key, mode, selecting_build_id,
	status, error_message, created_at, updated_at`

const agentWorkflowResourceSourceBindingColumns = `
	admission_id, source_name, resource_name, selecting_build_id,
	source_pipeline_id, pipeline_config_version, resource_id,
	resource_config_version_id, resource_version_id, version_digest,
	version, snapshot_type_name, snapshot_type_version,
	capture_operation_key, snapshot_id, created_at, captured_at`

// NewWorkflowResourceSourceAdmissionStore is the runtime half of the Task 12
// persistence model. Its only selection input is an already-adopted scheduler
// build; callers cannot ask it to select a current or latest version.
func NewWorkflowResourceSourceAdmissionStore(conn DbConn) WorkflowResourceSourceAdmissionStore {
	return &workflowResourceSourceAdmissionStore{conn: conn}
}

type workflowResourceSourceAdmissionStore struct{ conn DbConn }

func (store *workflowResourceSourceAdmissionStore) CreateManual(
	ctx context.Context,
	teamID int,
	identity ManualAdmissionIdentity,
) (AgentWorkflowResourceSourceAdmission, bool, error) {
	if ctx == nil || teamID <= 0 {
		return AgentWorkflowResourceSourceAdmission{}, false, fmt.Errorf("db: manual source admission requires context and team")
	}
	if err := identity.Validate(); err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	return store.create(ctx, teamID, identity.WorkflowDefinitionID, identity.SourcePipelineID, identity.SourceConfigHash, identity.IdempotencyKey, AgentWorkflowResourceSourceAdmissionManual, nil)
}

func (store *workflowResourceSourceAdmissionStore) ClaimBuild(
	ctx context.Context,
	teamID, sourcePipelineID int,
	selectingBuildID int64,
	claim BuildClaim,
) (AgentWorkflowResourceSourceAdmission, bool, error) {
	if ctx == nil || teamID <= 0 || sourcePipelineID <= 0 || selectingBuildID <= 0 {
		return AgentWorkflowResourceSourceAdmission{}, false, fmt.Errorf("db: automatic source admission claim requires context and identities")
	}
	if err := claim.Validate(); err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	return store.create(ctx, teamID, claim.WorkflowDefinitionID, sourcePipelineID, claim.SourceConfigHash, claim.IdempotencyKey, AgentWorkflowResourceSourceAdmissionAutomatic, &selectingBuildID)
}

func (store *workflowResourceSourceAdmissionStore) create(
	ctx context.Context,
	teamID, definitionID, pipelineID int,
	configHash, idempotencyKey string,
	mode AgentWorkflowResourceSourceAdmissionMode,
	selectingBuildID *int64,
) (AgentWorkflowResourceSourceAdmission, bool, error) {
	if err := mode.Validate(); err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	tx, err := store.conn.BeginTx(ctx, nil)
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	defer Rollback(tx)

	if err := lockWorkflowResourceSourceAdmission(ctx, tx, teamID, idempotencyKey); err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	existing, found, err := findWorkflowResourceSourceAdmissionByKey(ctx, tx, teamID, idempotencyKey, true)
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	if !found && selectingBuildID != nil {
		existing, found, err = findWorkflowResourceSourceAdmissionByBuild(ctx, tx, teamID, pipelineID, *selectingBuildID, true)
		if err != nil {
			return AgentWorkflowResourceSourceAdmission{}, false, err
		}
	}
	if found {
		if !sameWorkflowResourceSourceAdmissionIdentity(existing, definitionID, pipelineID, configHash, idempotencyKey, mode, selectingBuildID) {
			return AgentWorkflowResourceSourceAdmission{}, false, fmt.Errorf("%w: admission identity differs", ErrAgentWorkflowResourceSourceConflict)
		}
		if selectingBuildID != nil {
			registered, registeredFound, err := findWorkflowResourceSourcePipeline(ctx, tx, teamID, pipelineID, true)
			if err != nil {
				return AgentWorkflowResourceSourceAdmission{}, false, err
			}
			if !registeredFound || registered.WorkflowDefinitionID != definitionID || registered.ConfigHash != configHash {
				return AgentWorkflowResourceSourceAdmission{}, false, fmt.Errorf("%w: registered source pipeline drifted", ErrAgentWorkflowResourceSourceConflict)
			}
			if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, registered); err != nil {
				return AgentWorkflowResourceSourceAdmission{}, false, err
			}
			if err := validateWorkflowResourceSourceBuildAuthority(ctx, tx, teamID, pipelineID, *selectingBuildID, registered.PipelineConfigVersion, mode, existing.ID); err != nil {
				return AgentWorkflowResourceSourceAdmission{}, false, err
			}
		}
		if err := tx.Commit(); err != nil {
			return AgentWorkflowResourceSourceAdmission{}, false, err
		}
		return existing, false, nil
	}

	registered, registeredFound, err := findWorkflowResourceSourcePipeline(ctx, tx, teamID, pipelineID, true)
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	if !registeredFound || registered.State != AgentWorkflowResourceSourcePipelineActive {
		return AgentWorkflowResourceSourceAdmission{}, false, ErrAgentWorkflowResourceSourceInactive
	}
	if registered.WorkflowDefinitionID != definitionID || registered.ConfigHash != configHash {
		return AgentWorkflowResourceSourceAdmission{}, false, fmt.Errorf("%w: admission does not match registered source pipeline", ErrAgentWorkflowResourceSourceConflict)
	}
	if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, registered); err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	if selectingBuildID != nil {
		if err := validateWorkflowResourceSourceBuildAuthority(ctx, tx, teamID, pipelineID, *selectingBuildID, registered.PipelineConfigVersion, mode, 0); err != nil {
			return AgentWorkflowResourceSourceAdmission{}, false, err
		}
	}

	result, err := tx.ExecContext(ctx, `
		INSERT INTO agent_workflow_resource_source_admissions
			(team_id, workflow_definition_id, source_pipeline_id, source_config_hash,
			 idempotency_key, mode, selecting_build_id, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'selecting') ON CONFLICT DO NOTHING`,
		teamID, definitionID, pipelineID, configHash, idempotencyKey, string(mode), nullableSourceBuildID(selectingBuildID))
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	created := affected == 1
	admission, found, err := findWorkflowResourceSourceAdmissionByKey(ctx, tx, teamID, idempotencyKey, true)
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	if !found && selectingBuildID != nil {
		admission, found, err = findWorkflowResourceSourceAdmissionByBuild(ctx, tx, teamID, pipelineID, *selectingBuildID, true)
		if err != nil {
			return AgentWorkflowResourceSourceAdmission{}, false, err
		}
	}
	if !found || !sameWorkflowResourceSourceAdmissionIdentity(admission, definitionID, pipelineID, configHash, idempotencyKey, mode, selectingBuildID) {
		return AgentWorkflowResourceSourceAdmission{}, false, fmt.Errorf("%w: admission identity differs", ErrAgentWorkflowResourceSourceConflict)
	}
	if err := tx.Commit(); err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	return admission, created, nil
}

// BindSelection never persists the caller's mapping. It derives the full
// mapping from the Task 12 frozen declaration plus the adopted build inputs,
// then requires caller evidence to match it exactly.
func (store *workflowResourceSourceAdmissionStore) BindSelection(
	ctx context.Context,
	teamID int,
	admissionID, selectingBuildID int64,
	selections []SelectedSource,
) (bool, error) {
	if ctx == nil || teamID <= 0 || admissionID <= 0 || selectingBuildID <= 0 || len(selections) == 0 {
		return false, fmt.Errorf("db: source selection requires context and identities")
	}
	provided, err := canonicalSelectedSources(selections)
	if err != nil {
		return false, err
	}
	tx, err := store.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	admission, found, err := findWorkflowResourceSourceAdmission(ctx, tx, teamID, admissionID, true)
	if err != nil {
		return false, err
	}
	if !found || admission.Status == AgentWorkflowResourceSourceAdmissionFailed {
		return false, fmt.Errorf("%w: admission cannot bind a selection", ErrAgentWorkflowResourceSourceConflict)
	}
	if admission.SelectingBuildID != nil && *admission.SelectingBuildID != selectingBuildID {
		return false, fmt.Errorf("%w: selecting build cannot change", ErrAgentWorkflowResourceSourceConflict)
	}
	registered, registeredFound, err := findWorkflowResourceSourcePipeline(ctx, tx, teamID, admission.SourcePipelineID, true)
	if err != nil {
		return false, err
	}
	if !registeredFound || registered.WorkflowDefinitionID != admission.WorkflowDefinitionID || registered.ConfigHash != admission.SourceConfigHash {
		return false, fmt.Errorf("%w: registered source pipeline drifted", ErrAgentWorkflowResourceSourceConflict)
	}
	if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, registered); err != nil {
		return false, err
	}
	if err := validateWorkflowResourceSourceBuildAuthority(ctx, tx, teamID, admission.SourcePipelineID, selectingBuildID, registered.PipelineConfigVersion, admission.Mode, admission.ID); err != nil {
		return false, err
	}
	derived, err := deriveSourceBindings(ctx, tx, CreateWorkflowResourceSourceAdmissionRequest{
		TeamID: teamID, WorkflowDefinitionID: admission.WorkflowDefinitionID, SourcePipelineID: admission.SourcePipelineID,
		SourceConfigHash: admission.SourceConfigHash, SelectingBuildID: selectingBuildID,
	}, registered.PipelineConfigVersion, registered.SourceDeclarations)
	if err != nil {
		return false, err
	}
	canonical := canonicalDerivedSelectedSources(derived, selectingBuildID)
	if !sameSelectedSources(provided, canonical) {
		return false, fmt.Errorf("%w: caller selection does not match frozen source declaration and exact build inputs", ErrAgentWorkflowResourceSourceConflict)
	}
	if admission.Status != AgentWorkflowResourceSourceAdmissionSelecting {
		matches, err := verifyWorkflowResourceSourceSelection(ctx, tx, admission, selectingBuildID, canonical)
		if err != nil {
			return false, err
		}
		if !matches {
			return false, fmt.Errorf("%w: bound selection differs", ErrAgentWorkflowResourceSourceConflict)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	for _, selected := range canonical {
		version, err := json.Marshal(selected.Version)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agent_workflow_resource_source_bindings
				(admission_id,source_name,resource_name,selecting_build_id,source_pipeline_id,pipeline_config_version,resource_id,resource_config_version_id,resource_version_id,version_digest,version,snapshot_type_name,snapshot_type_version,capture_operation_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			admission.ID, selected.SourceName, selected.ResourceName, selectingBuildID,
			admission.SourcePipelineID, registered.PipelineConfigVersion, selected.ResourceID,
			selected.ResourceConfigVersionID, selected.ResourceVersionID, selected.VersionDigest,
			version, snapshotTypeName(selected.SnapshotType), snapshotTypeVersion(selected.SnapshotType), selected.CaptureOperationKey)
		if err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_resource_source_admissions
		SET selecting_build_id=$3,status='capturing',updated_at=now()
		WHERE id=$1 AND team_id=$2 AND status='selecting'
		  AND (selecting_build_id IS NULL OR selecting_build_id=$3)`, admission.ID, teamID, selectingBuildID)
	if err != nil {
		return false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err == nil {
			err = fmt.Errorf("%w: selection changed concurrently", ErrAgentWorkflowResourceSourceConflict)
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (store *workflowResourceSourceAdmissionStore) BindCapture(
	ctx context.Context,
	teamID int,
	admissionID int64,
	sourceName string,
	snapshotID snapshot.SnapshotID,
) (bool, error) {
	if ctx == nil || teamID <= 0 || admissionID <= 0 || strings.TrimSpace(sourceName) == "" {
		return false, fmt.Errorf("db: source capture requires context and identities")
	}
	if err := snapshotID.Validate(); err != nil {
		return false, err
	}
	tx, err := store.conn.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer Rollback(tx)
	admission, found, err := findWorkflowResourceSourceAdmission(ctx, tx, teamID, admissionID, true)
	if err != nil {
		return false, err
	}
	if !found || admission.Status == AgentWorkflowResourceSourceAdmissionSelecting ||
		admission.Status == AgentWorkflowResourceSourceAdmissionFailed {
		return false, fmt.Errorf("%w: admission cannot accept captures", ErrAgentWorkflowResourceSourceConflict)
	}
	binding, found, err := findWorkflowResourceSourceBinding(ctx, tx, admissionID, sourceName, true)
	if err != nil {
		return false, err
	}
	if !found {
		return false, fmt.Errorf("%w: source binding does not exist", ErrAgentWorkflowResourceSourceConflict)
	}
	if err := validateBoundSourceSnapshot(ctx, tx, teamID, binding.SnapshotType, snapshotID); err != nil {
		return false, err
	}
	if binding.SnapshotID != nil {
		if *binding.SnapshotID != snapshotID {
			return false, fmt.Errorf("%w: captured snapshot cannot be overwritten", ErrAgentWorkflowResourceSourceConflict)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if admission.Status != AgentWorkflowResourceSourceAdmissionCapturing {
		return false, fmt.Errorf("%w: ready admission cannot accept a new capture", ErrAgentWorkflowResourceSourceConflict)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_resource_source_bindings SET snapshot_id=$3,captured_at=now()
		WHERE admission_id=$1 AND source_name=$2 AND snapshot_id IS NULL`, admissionID, sourceName, int64(snapshotID))
	if err != nil {
		return false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err == nil {
			err = fmt.Errorf("%w: capture binding changed concurrently", ErrAgentWorkflowResourceSourceConflict)
		}
		return false, err
	}
	var incomplete int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_workflow_resource_source_bindings WHERE admission_id=$1 AND snapshot_id IS NULL`, admissionID).Scan(&incomplete); err != nil {
		return false, err
	}
	if incomplete == 0 {
		result, err = tx.ExecContext(ctx, `UPDATE agent_workflow_resource_source_admissions SET status='ready',updated_at=now() WHERE id=$1 AND team_id=$2 AND status='capturing'`, admissionID, teamID)
		if err != nil {
			return false, err
		}
		if affected, err := result.RowsAffected(); err != nil || affected != 1 {
			if err == nil {
				err = fmt.Errorf("%w: readiness changed concurrently", ErrAgentWorkflowResourceSourceConflict)
			}
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (store *workflowResourceSourceAdmissionStore) Ready(ctx context.Context, teamID int, admissionID int64) (ReadySourceAdmission, bool, error) {
	if ctx == nil || teamID <= 0 || admissionID <= 0 {
		return ReadySourceAdmission{}, false, fmt.Errorf("db: ready source admission requires context and identities")
	}
	admission, found, err := findWorkflowResourceSourceAdmission(ctx, store.conn, teamID, admissionID, false)
	if err != nil || !found {
		return ReadySourceAdmission{}, false, err
	}
	if admission.Status != AgentWorkflowResourceSourceAdmissionReady {
		return ReadySourceAdmission{}, false, nil
	}
	rows, err := store.conn.QueryContext(ctx, `
		SELECT `+agentWorkflowResourceSourceBindingColumns+`, EXISTS (
			SELECT 1 FROM agent_snapshots snapshot
			WHERE snapshot.id=binding.snapshot_id AND snapshot.team_id=$2
			  AND snapshot.type_name=binding.snapshot_type_name
			  AND snapshot.type_version=binding.snapshot_type_version
			  AND snapshot.content_state='available')
		FROM agent_workflow_resource_source_bindings binding
		WHERE admission_id=$1 ORDER BY source_name`, admissionID, teamID)
	if err != nil {
		return ReadySourceAdmission{}, false, err
	}
	defer Close(rows)
	bindings := make([]AgentWorkflowResourceSourceBinding, 0)
	for rows.Next() {
		var available bool
		binding, err := scanWorkflowResourceSourceBindingWithExtra(rows, &available)
		if err != nil {
			return ReadySourceAdmission{}, false, err
		}
		if binding.SnapshotID == nil || !available {
			return ReadySourceAdmission{}, false, fmt.Errorf("%w: ready binding snapshot is unavailable or unauthorized", ErrAgentWorkflowResourceSourceConflict)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return ReadySourceAdmission{}, false, err
	}
	if len(bindings) == 0 {
		return ReadySourceAdmission{}, false, fmt.Errorf("%w: ready admission has no bindings", ErrAgentWorkflowResourceSourceConflict)
	}
	return ReadySourceAdmission{Admission: admission, Bindings: bindings}, true, nil
}

func (store *workflowResourceSourceAdmissionStore) Capturing(ctx context.Context, teamID int, admissionID int64) (CapturingSourceAdmission, bool, error) {
	if ctx == nil || teamID <= 0 || admissionID <= 0 {
		return CapturingSourceAdmission{}, false, fmt.Errorf("db: capturing source admission requires context and identities")
	}
	admission, found, err := findWorkflowResourceSourceAdmission(ctx, store.conn, teamID, admissionID, false)
	if err != nil || !found {
		return CapturingSourceAdmission{}, false, err
	}
	if admission.Status != AgentWorkflowResourceSourceAdmissionCapturing {
		return CapturingSourceAdmission{}, false, nil
	}
	registered, found, err := findWorkflowResourceSourcePipeline(ctx, store.conn, teamID, admission.SourcePipelineID, false)
	if err != nil {
		return CapturingSourceAdmission{}, false, err
	}
	if !found || registered.WorkflowDefinitionID != admission.WorkflowDefinitionID || registered.ConfigHash != admission.SourceConfigHash {
		return CapturingSourceAdmission{}, false, fmt.Errorf("%w: capturing source pipeline drifted", ErrAgentWorkflowResourceSourceConflict)
	}
	return store.loadCapturingSourceAdmission(
		ctx, teamID, admissionID, admission, registered,
	)
}

func (store *workflowResourceSourceAdmissionStore) loadCapturingSourceAdmission(
	ctx context.Context,
	teamID int,
	admissionID int64,
	admission AgentWorkflowResourceSourceAdmission,
	registered WorkflowResourceSourcePipeline,
) (CapturingSourceAdmission, bool, error) {
	var teamName, pipelineName string
	var instanceVars []byte
	err := store.conn.QueryRowContext(ctx, `
		SELECT team.name,pipeline.name,pipeline.instance_vars
		FROM pipelines pipeline JOIN teams team ON team.id=pipeline.team_id
		WHERE pipeline.id=$1 AND pipeline.team_id=$2`, admission.SourcePipelineID, teamID).Scan(&teamName, &pipelineName, &instanceVars)
	if errors.Is(err, sql.ErrNoRows) {
		return CapturingSourceAdmission{}, false, fmt.Errorf("%w: source pipeline is unavailable", ErrAgentWorkflowResourceSourceConflict)
	}
	if err != nil {
		return CapturingSourceAdmission{}, false, err
	}
	var parsedVars atc.InstanceVars
	if len(instanceVars) != 0 {
		if err := json.Unmarshal(instanceVars, &parsedVars); err != nil {
			return CapturingSourceAdmission{}, false, fmt.Errorf("db: decode source pipeline instance vars: %w", err)
		}
	}
	rows, err := store.conn.QueryContext(ctx, `SELECT `+agentWorkflowResourceSourceBindingColumns+` FROM agent_workflow_resource_source_bindings WHERE admission_id=$1 ORDER BY source_name`, admissionID)
	if err != nil {
		return CapturingSourceAdmission{}, false, err
	}
	defer Close(rows)
	bindings := make([]AgentWorkflowResourceSourceBinding, 0)
	for rows.Next() {
		binding, err := scanWorkflowResourceSourceBinding(rows)
		if err != nil {
			return CapturingSourceAdmission{}, false, err
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return CapturingSourceAdmission{}, false, err
	}
	if len(bindings) == 0 {
		return CapturingSourceAdmission{}, false, fmt.Errorf("%w: capturing admission has no bindings", ErrAgentWorkflowResourceSourceConflict)
	}
	return CapturingSourceAdmission{Admission: admission, PipelineRegistration: registered, TeamName: teamName, Pipeline: atc.PipelineRef{Name: pipelineName, InstanceVars: parsedVars}, Bindings: bindings}, true, nil
}

func validateBoundSourceSnapshot(ctx context.Context, queryer snapshotQueryer, teamID int, want snapshot.TypeRef, id snapshot.SnapshotID) error {
	var ownerID, typeVersion int
	var typeName, state string
	err := queryer.QueryRowContext(ctx, `SELECT team_id,type_name,type_version,content_state FROM agent_snapshots WHERE id=$1 FOR SHARE`, int64(id)).Scan(&ownerID, &typeName, &typeVersion, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: captured snapshot does not exist", ErrAgentWorkflowResourceSourceConflict)
	}
	if err != nil {
		return err
	}
	if ownerID != teamID || typeName != snapshotTypeName(want) || typeVersion != snapshotTypeVersion(want) || state != "available" {
		return fmt.Errorf("%w: captured snapshot owner, type, or availability does not match binding", ErrAgentWorkflowResourceSourceConflict)
	}
	return nil
}

func canonicalDerivedSelectedSources(bindings []derivedResourceSourceBinding, buildID int64) []SelectedSource {
	values := make([]SelectedSource, 0, len(bindings))
	for _, binding := range bindings {
		values = append(values, SelectedSource{SourceName: binding.SourceName, ResourceName: binding.ResourceName, SelectingBuildID: buildID, ResourceID: binding.ResourceID, ResourceConfigVersionID: binding.ResourceConfigVersionID, ResourceVersionID: binding.ResourceConfigVersionID, VersionDigest: binding.VersionDigest, Version: cloneWorkflowResourceSourceVersion(binding.Version), SnapshotType: binding.SnapshotType, CaptureOperationKey: binding.CaptureOperationKey})
	}
	sort.Slice(values, func(left, right int) bool { return values[left].SourceName < values[right].SourceName })
	return values
}

func canonicalSelectedSources(sources []SelectedSource) ([]SelectedSource, error) {
	values := make([]SelectedSource, len(sources))
	seen := make(map[string]struct{}, len(sources))
	for index, source := range sources {
		if err := source.Validate(); err != nil {
			return nil, err
		}
		if _, duplicate := seen[source.SourceName]; duplicate {
			return nil, fmt.Errorf("db: duplicate selected source %q", source.SourceName)
		}
		seen[source.SourceName] = struct{}{}
		values[index] = source
		values[index].Version = cloneWorkflowResourceSourceVersion(source.Version)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].SourceName < values[right].SourceName })
	return values, nil
}

func sameSelectedSources(left, right []SelectedSource) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].SourceName != right[index].SourceName || left[index].ResourceName != right[index].ResourceName || left[index].SelectingBuildID != right[index].SelectingBuildID || left[index].ResourceID != right[index].ResourceID || left[index].ResourceConfigVersionID != right[index].ResourceConfigVersionID || left[index].ResourceVersionID != right[index].ResourceVersionID || left[index].VersionDigest != right[index].VersionDigest || left[index].SnapshotType != right[index].SnapshotType || left[index].CaptureOperationKey != right[index].CaptureOperationKey || !sameWorkflowResourceSourceVersion(left[index].Version, right[index].Version) {
			return false
		}
	}
	return true
}

func verifyWorkflowResourceSourceSelection(ctx context.Context, queryer snapshotQueryer, admission AgentWorkflowResourceSourceAdmission, buildID int64, selections []SelectedSource) (bool, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT `+agentWorkflowResourceSourceBindingColumns+` FROM agent_workflow_resource_source_bindings WHERE admission_id=$1 ORDER BY source_name`, admission.ID)
	if err != nil {
		return false, err
	}
	defer Close(rows)
	bindings := make([]AgentWorkflowResourceSourceBinding, 0)
	for rows.Next() {
		binding, err := scanWorkflowResourceSourceBinding(rows)
		if err != nil {
			return false, err
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(bindings) != len(selections) {
		return false, nil
	}
	for index, binding := range bindings {
		selection := selections[index]
		if binding.SelectingBuildID != buildID || binding.SourceName != selection.SourceName || binding.ResourceName != selection.ResourceName || binding.ResourceID != selection.ResourceID || binding.ResourceConfigVersionID != selection.ResourceConfigVersionID || binding.ResourceVersionID != selection.ResourceVersionID || binding.VersionDigest != selection.VersionDigest || binding.SnapshotType != selection.SnapshotType || binding.CaptureOperationKey != selection.CaptureOperationKey || !sameWorkflowResourceSourceVersion(binding.Version, selection.Version) {
			return false, nil
		}
	}
	return true, nil
}

func findWorkflowResourceSourceAdmission(ctx context.Context, queryer snapshotQueryer, teamID int, admissionID int64, lock bool) (AgentWorkflowResourceSourceAdmission, bool, error) {
	query := `SELECT ` + agentWorkflowResourceSourceAdmissionColumns + ` FROM agent_workflow_resource_source_admissions WHERE team_id=$1 AND id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWorkflowResourceSourceAdmissionFound(queryer.QueryRowContext(ctx, query, teamID, admissionID))
}

func findWorkflowResourceSourceAdmissionByKey(ctx context.Context, queryer snapshotQueryer, teamID int, key string, lock bool) (AgentWorkflowResourceSourceAdmission, bool, error) {
	query := `SELECT ` + agentWorkflowResourceSourceAdmissionColumns + ` FROM agent_workflow_resource_source_admissions WHERE team_id=$1 AND idempotency_key=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWorkflowResourceSourceAdmissionFound(queryer.QueryRowContext(ctx, query, teamID, key))
}

func findWorkflowResourceSourceAdmissionByBuild(ctx context.Context, queryer snapshotQueryer, teamID, pipelineID int, buildID int64, lock bool) (AgentWorkflowResourceSourceAdmission, bool, error) {
	query := `SELECT ` + agentWorkflowResourceSourceAdmissionColumns + ` FROM agent_workflow_resource_source_admissions WHERE team_id=$1 AND source_pipeline_id=$2 AND selecting_build_id=$3`
	if lock {
		query += ` FOR UPDATE`
	}
	return scanWorkflowResourceSourceAdmissionFound(queryer.QueryRowContext(ctx, query, teamID, pipelineID, buildID))
}

func scanWorkflowResourceSourceAdmissionFound(row interface{ Scan(...any) error }) (AgentWorkflowResourceSourceAdmission, bool, error) {
	var admission AgentWorkflowResourceSourceAdmission
	var selectingBuild sql.NullInt64
	err := row.Scan(&admission.ID, &admission.TeamID, &admission.WorkflowDefinitionID, &admission.SourcePipelineID, &admission.SourceConfigHash, &admission.IdempotencyKey, &admission.Mode, &selectingBuild, &admission.Status, &admission.ErrorMessage, &admission.CreatedAt, &admission.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowResourceSourceAdmission{}, false, nil
	}
	if err != nil {
		return AgentWorkflowResourceSourceAdmission{}, false, err
	}
	if selectingBuild.Valid {
		value := selectingBuild.Int64
		admission.SelectingBuildID = &value
	}
	return admission, true, nil
}

func findWorkflowResourceSourceBinding(ctx context.Context, queryer snapshotQueryer, admissionID int64, sourceName string, lock bool) (AgentWorkflowResourceSourceBinding, bool, error) {
	query := `SELECT ` + agentWorkflowResourceSourceBindingColumns + ` FROM agent_workflow_resource_source_bindings WHERE admission_id=$1 AND source_name=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	binding, err := scanWorkflowResourceSourceBinding(queryer.QueryRowContext(ctx, query, admissionID, sourceName))
	if errors.Is(err, sql.ErrNoRows) {
		return AgentWorkflowResourceSourceBinding{}, false, nil
	}
	return binding, err == nil, err
}

func scanWorkflowResourceSourceBinding(row interface{ Scan(...any) error }) (AgentWorkflowResourceSourceBinding, error) {
	return scanWorkflowResourceSourceBindingWithExtra(row)
}

func scanWorkflowResourceSourceBindingWithExtra(row interface{ Scan(...any) error }, extra ...any) (AgentWorkflowResourceSourceBinding, error) {
	var binding AgentWorkflowResourceSourceBinding
	var versionJSON []byte
	var typeName string
	var typeVersion int
	var snapshotID sql.NullInt64
	var capturedAt sql.NullTime
	destinations := []any{&binding.AdmissionID, &binding.SourceName, &binding.ResourceName, &binding.SelectingBuildID, &binding.SourcePipelineID, &binding.PipelineConfigVersion, &binding.ResourceID, &binding.ResourceConfigVersionID, &binding.ResourceVersionID, &binding.VersionDigest, &versionJSON, &typeName, &typeVersion, &binding.CaptureOperationKey, &snapshotID, &binding.CreatedAt, &capturedAt}
	destinations = append(destinations, extra...)
	if err := row.Scan(destinations...); err != nil {
		return AgentWorkflowResourceSourceBinding{}, err
	}
	if err := json.Unmarshal(versionJSON, &binding.Version); err != nil {
		return AgentWorkflowResourceSourceBinding{}, fmt.Errorf("db: decode source binding version: %w", err)
	}
	typeRef, err := snapshot.ParseTypeRef(typeName + "/v" + strconv.Itoa(typeVersion))
	if err != nil {
		return AgentWorkflowResourceSourceBinding{}, err
	}
	binding.SnapshotType = typeRef
	if snapshotID.Valid {
		value := snapshot.SnapshotID(snapshotID.Int64)
		binding.SnapshotID = &value
	}
	if capturedAt.Valid {
		value := capturedAt.Time
		binding.CapturedAt = &value
	}
	return binding, nil
}

func findWorkflowResourceSourcePipeline(ctx context.Context, queryer snapshotQueryer, teamID, pipelineID int, lock bool) (WorkflowResourceSourcePipeline, bool, error) {
	query := `SELECT pipeline_id,team_id,workflow_definition_id,workflow_name,workflow_version,pipeline_config_version,config_hash,source_declarations,state FROM agent_workflow_resource_source_pipelines WHERE team_id=$1 AND pipeline_id=$2`
	if lock {
		query += ` FOR UPDATE`
	}
	var pipeline WorkflowResourceSourcePipeline
	var declarations []byte
	err := queryer.QueryRowContext(ctx, query, teamID, pipelineID).Scan(&pipeline.PipelineID, &pipeline.TeamID, &pipeline.WorkflowDefinitionID, &pipeline.WorkflowName, &pipeline.WorkflowVersion, &pipeline.PipelineConfigVersion, &pipeline.ConfigHash, &declarations, &pipeline.State)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkflowResourceSourcePipeline{}, false, nil
	}
	if err != nil {
		return WorkflowResourceSourcePipeline{}, false, err
	}
	if err := json.Unmarshal(declarations, &pipeline.SourceDeclarations); err != nil {
		return WorkflowResourceSourcePipeline{}, false, fmt.Errorf("db: decode source declarations: %w", err)
	}
	if err := validateSourceDeclarations(pipeline.SourceDeclarations); err != nil {
		return WorkflowResourceSourcePipeline{}, false, err
	}
	return pipeline, true, nil
}

func validateWorkflowResourceSourcePipelineAuthority(ctx context.Context, queryer snapshotQueryer, pipeline WorkflowResourceSourcePipeline) error {
	var valid bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pipelines WHERE id=$1 AND team_id=$2 AND NOT template AND NOT archived AND version=$3)`, pipeline.PipelineID, pipeline.TeamID, pipeline.PipelineConfigVersion).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("%w: registered pipeline does not match its active ordinary config revision", ErrAgentWorkflowResourceSourceConflict)
	}
	return nil
}

func validateWorkflowResourceSourceBuildAuthority(ctx context.Context, queryer snapshotQueryer, teamID, pipelineID int, buildID int64, configVersion int, mode AgentWorkflowResourceSourceAdmissionMode, admissionID int64) error {
	var status string
	var completed, aborted, manuallyTriggered bool
	var ended sql.NullTime
	var observedConfig sql.NullInt64
	err := queryer.QueryRowContext(ctx, `
		SELECT build.status,build.completed,build.aborted,build.end_time,build.pipeline_config_version,build.manually_triggered
		FROM builds build JOIN jobs job ON job.id=build.job_id JOIN pipelines pipeline ON pipeline.id=build.pipeline_id
		WHERE build.id=$1 AND build.team_id=$2 AND build.pipeline_id=$3 AND job.pipeline_id=$3 AND job.name='admit' AND NOT pipeline.template FOR UPDATE OF build`, buildID, teamID, pipelineID).Scan(&status, &completed, &aborted, &ended, &observedConfig, &manuallyTriggered)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: selecting build is not the registered pipeline's admit job", ErrAgentWorkflowResourceSourceConflict)
	}
	if err != nil {
		return err
	}
	if status != string(BuildStatusSucceeded) || !completed || aborted || !ended.Valid || !observedConfig.Valid || observedConfig.Int64 != int64(configVersion) {
		return fmt.Errorf("%w: selecting build is not a completed non-aborted success at the registered config version", ErrAgentWorkflowResourceSourceConflict)
	}
	wantManual := mode == AgentWorkflowResourceSourceAdmissionManual
	if manuallyTriggered != wantManual {
		return fmt.Errorf("%w: selecting build origin does not match admission mode", ErrAgentWorkflowResourceSourceConflict)
	}
	if admissionID == 0 {
		return nil
	}
	var owned bool
	err = queryer.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM agent_workflow_resource_source_admissions WHERE id=$1 AND team_id=$2 AND source_pipeline_id=$3 AND selecting_build_id=$4 AND mode=$5)`, admissionID, teamID, pipelineID, buildID, string(mode)).Scan(&owned)
	if err != nil {
		return err
	}
	if !owned {
		return fmt.Errorf("%w: selecting build is not owned by this admission", ErrAgentWorkflowResourceSourceConflict)
	}
	return nil
}

func sameWorkflowResourceSourceAdmissionIdentity(admission AgentWorkflowResourceSourceAdmission, definitionID, pipelineID int, configHash, key string, mode AgentWorkflowResourceSourceAdmissionMode, buildID *int64) bool {
	return admission.WorkflowDefinitionID == definitionID && admission.SourcePipelineID == pipelineID && admission.SourceConfigHash == configHash && admission.IdempotencyKey == key && admission.Mode == mode && ((mode == AgentWorkflowResourceSourceAdmissionManual && buildID == nil) || sameOptionalSourceBuildID(admission.SelectingBuildID, buildID))
}

func sameOptionalSourceBuildID(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func nullableSourceBuildID(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func cloneWorkflowResourceSourceVersion(version atc.Version) atc.Version {
	cloned := make(atc.Version, len(version))
	for key, value := range version {
		cloned[key] = value
	}
	return cloned
}

func sameWorkflowResourceSourceVersion(left, right atc.Version) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func lockWorkflowResourceSourceAdmission(ctx context.Context, tx Tx, teamID int, key string) error {
	lockID := snapshotAdvisoryLockKey("workflow-resource-source-admission/v1\x00", fmt.Sprintf("%d\x00%s", teamID, key))
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, lockID)
	return err
}
