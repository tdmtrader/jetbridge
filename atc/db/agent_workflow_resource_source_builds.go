package db

import (
	"context"
	"crypto/md5" // #nosec G501 -- validates legacy persisted version digests.
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3/lagerctx"
	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db/lock"
)

type workflowResourceSourceBuildStore struct {
	conn         DbConn
	lockFactory  lock.LockFactory
	checkFactory CheckFactory
}

func NewWorkflowResourceSourceBuildStore(conn DbConn, lockFactory lock.LockFactory, checkFactory CheckFactory) WorkflowResourceSourceBuildStore {
	return &workflowResourceSourceBuildStore{conn: conn, lockFactory: lockFactory, checkFactory: checkFactory}
}

// EnsureManualBuild creates a normal manually-triggered `admit` build within
// the same transaction that attaches it to the admission. Retried dispatches
// read the attachment and never create a second selector build.
func (store *workflowResourceSourceBuildStore) EnsureManualBuild(ctx context.Context, teamID int, admissionID int64, sourcePipelineID int) (int, bool, error) {
	if ctx == nil || teamID <= 0 || admissionID <= 0 || sourcePipelineID <= 0 {
		return 0, false, fmt.Errorf("db: manual source build requires context and identities")
	}
	tx, err := store.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer Rollback(tx)
	admission, found, err := findWorkflowResourceSourceAdmission(ctx, tx, teamID, admissionID, true)
	if err != nil {
		return 0, false, err
	}
	if !found || admission.Mode != AgentWorkflowResourceSourceAdmissionManual || admission.SourcePipelineID != sourcePipelineID {
		return 0, false, fmt.Errorf("%w: manual admission does not own source pipeline", ErrAgentWorkflowResourceSourceConflict)
	}
	if admission.SelectingBuildID != nil {
		if err := validateWorkflowResourceSourceSelectingBuild(ctx, tx, teamID, sourcePipelineID, *admission.SelectingBuildID); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return int(*admission.SelectingBuildID), false, nil
	}
	if admission.Status != AgentWorkflowResourceSourceAdmissionSelecting {
		return 0, false, fmt.Errorf("%w: manual admission has no selecting build", ErrAgentWorkflowResourceSourceConflict)
	}
	job, found, err := workflowResourceSourceAdmitJob(store.conn, store.lockFactory, tx, sourcePipelineID)
	if err != nil {
		return 0, false, err
	}
	if !found || job.teamID != teamID {
		return 0, false, fmt.Errorf("%w: source pipeline has no active admit job", ErrAgentWorkflowResourceSourceConflict)
	}
	build, err := job.createWorkflowResourceSourceAdmissionBuild(tx)
	if err != nil {
		return 0, false, err
	}
	buildID := int64(build.ID())
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_workflow_resource_source_admissions SET selecting_build_id=$3,updated_at=now()
		WHERE id=$1 AND team_id=$2 AND mode='manual' AND status='selecting' AND selecting_build_id IS NULL`, admissionID, teamID, buildID)
	if err != nil {
		return 0, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		if err == nil {
			err = fmt.Errorf("%w: manual build attachment changed concurrently", ErrAgentWorkflowResourceSourceConflict)
		}
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return build.ID(), true, nil
}

func (store *workflowResourceSourceBuildStore) RequestPostDispatchChecks(ctx context.Context, teamID, sourcePipelineID, buildID int) error {
	if ctx == nil || teamID <= 0 || sourcePipelineID <= 0 || buildID <= 0 {
		return fmt.Errorf("db: source post-dispatch checks require context and identities")
	}
	if store.checkFactory == nil {
		return fmt.Errorf("db: source post-dispatch check factory is required")
	}
	if err := store.validateManualSourceBuild(ctx, teamID, sourcePipelineID, buildID); err != nil {
		return err
	}
	resources, err := resources(sourcePipelineID, store.conn, store.lockFactory)
	if err != nil {
		return err
	}
	resourceTypes, err := sourceBuildResourceTypes(store.conn, store.lockFactory, sourcePipelineID)
	if err != nil {
		return err
	}
	inputIDs, err := sourceBuildUnpinnedInputResourceIDs(ctx, store.conn, sourcePipelineID)
	if err != nil {
		return err
	}
	for _, resource := range resources {
		if _, input := inputIDs[resource.ID()]; !input {
			continue
		}
		if _, _, err := store.checkFactory.TryCreateCheck(lagerctx.NewContext(ctx, lagerctx.FromContext(ctx)), resource, resourceTypes, nil, true, true, true); err != nil {
			return fmt.Errorf("request post-dispatch check for resource %q: %w", resource.Name(), err)
		}
	}
	return nil
}

func (store *workflowResourceSourceBuildStore) SuccessfulUnclaimedBuilds(ctx context.Context, teamID, sourcePipelineID int) ([]SourceBuild, error) {
	if ctx == nil || teamID <= 0 || sourcePipelineID <= 0 {
		return nil, fmt.Errorf("db: source successful-build read requires context and identities")
	}
	rows, err := store.conn.QueryContext(ctx, `
		SELECT build.id,build.pipeline_id,build.job_id,build.team_id,build.manually_triggered,admission.id,admission.mode,admission.status
		FROM builds build
		JOIN jobs job ON job.id=build.job_id
		JOIN pipelines pipeline ON pipeline.id=build.pipeline_id
		JOIN agent_workflow_resource_source_pipelines registered ON registered.team_id=build.team_id AND registered.pipeline_id=build.pipeline_id AND build.pipeline_config_version=registered.pipeline_config_version
		LEFT JOIN agent_workflow_resource_source_admissions admission ON admission.team_id=build.team_id AND admission.source_pipeline_id=build.pipeline_id AND admission.selecting_build_id=build.id
		WHERE build.team_id=$1 AND build.pipeline_id=$2 AND job.pipeline_id=$2 AND job.name='admit' AND NOT pipeline.template
		  AND build.status='succeeded' AND build.completed AND NOT build.aborted AND build.end_time IS NOT NULL
		  AND ((NOT build.manually_triggered AND admission.id IS NULL) OR
		       ((admission.status IN ('selecting','capturing') OR (admission.status='ready' AND admission.mode='automatic'))
		         AND ((build.manually_triggered AND admission.mode='manual') OR (NOT build.manually_triggered AND admission.mode='automatic'))
		         AND NOT EXISTS (
				SELECT 1 FROM agent_workflow_runs run
				WHERE run.resource_source_admission_id=admission.id
				  AND NOT (
					run.status='admitting' AND run.pipeline_run_id IS NULL
					AND run.template_pipeline_id IS NULL AND run.instance_pipeline_id IS NULL
					AND run.concrete_config IS NULL AND run.concrete_config_hash IS NULL
					AND run.planned_build_id IS NULL
				  )
			)))
		ORDER BY build.id`, teamID, sourcePipelineID)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	builds := make([]SourceBuild, 0)
	for rows.Next() {
		var build SourceBuild
		var admission sql.NullInt64
		var mode, status sql.NullString
		if err := rows.Scan(&build.ID, &build.PipelineID, &build.JobID, &build.TeamID, &build.ManuallyTriggered, &admission, &mode, &status); err != nil {
			return nil, err
		}
		if admission.Valid {
			value := admission.Int64
			build.AdmissionID = &value
			build.AdmissionMode = AgentWorkflowResourceSourceAdmissionMode(mode.String)
			build.AdmissionStatus = AgentWorkflowResourceSourceAdmissionStatus(status.String)
			if build.AdmissionMode.Validate() != nil || build.AdmissionStatus.Validate() != nil {
				return nil, fmt.Errorf("db: source build has invalid admission state")
			}
		}
		builds = append(builds, build)
	}
	return builds, rows.Err()
}

// ExactInputMapping reads adopted input rows only. It cannot invoke scheduler
// candidate selection or a latest resource-version lookup.
func (store *workflowResourceSourceBuildStore) ExactInputMapping(ctx context.Context, teamID, sourcePipelineID, buildID int) ([]SelectedSource, bool, error) {
	if ctx == nil || teamID <= 0 || sourcePipelineID <= 0 || buildID <= 0 {
		return nil, false, fmt.Errorf("db: source input mapping requires context and identities")
	}
	tx, err := store.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer Rollback(tx)
	registered, found, err := findWorkflowResourceSourcePipeline(ctx, tx, teamID, sourcePipelineID, false)
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, fmt.Errorf("%w: source pipeline is not registered", ErrAgentWorkflowResourceSourceConflict)
	}
	selections, selected, err := exactWorkflowResourceSourceInputMapping(
		ctx, tx, registered, buildID,
	)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return selections, selected, nil
}

func exactWorkflowResourceSourceInputMapping(
	ctx context.Context,
	tx Tx,
	registered WorkflowResourceSourcePipeline,
	buildID int,
) ([]SelectedSource, bool, error) {
	if err := validateWorkflowResourceSourcePipelineAuthority(ctx, tx, registered); err != nil {
		return nil, false, err
	}
	var matched bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM builds build JOIN jobs job ON job.id=build.job_id JOIN pipelines pipeline ON pipeline.id=build.pipeline_id
		WHERE build.id=$1 AND build.team_id=$2 AND build.pipeline_id=$3 AND job.pipeline_id=$3 AND job.name='admit' AND NOT pipeline.template
		  AND build.status='succeeded' AND build.completed AND NOT build.aborted AND build.end_time IS NOT NULL AND build.pipeline_config_version=$4
	)`, buildID, registered.TeamID, registered.PipelineID, registered.PipelineConfigVersion).Scan(&matched)
	if err != nil {
		return nil, false, err
	}
	if !matched {
		return nil, false, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT input.name,resource.name,resource.id,version.id,input.version_digest,version.version
		FROM build_resource_config_version_inputs input JOIN resources resource ON resource.id=input.resource_id
		JOIN resource_config_versions version ON version.resource_config_scope_id=resource.resource_config_scope_id AND input.version_digest IN (version.version_md5,version.version_sha256)
		WHERE input.build_id=$1 AND resource.pipeline_id=$2 ORDER BY input.name,version.id FOR SHARE OF input,resource,version`, buildID, registered.PipelineID)
	if err != nil {
		return nil, false, err
	}
	defer Close(rows)
	selections := make([]SelectedSource, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var selected SelectedSource
		var versionJSON []byte
		if err := rows.Scan(&selected.SourceName, &selected.ResourceName, &selected.ResourceID, &selected.ResourceConfigVersionID, &selected.VersionDigest, &versionJSON); err != nil {
			return nil, false, err
		}
		if _, duplicate := seen[selected.SourceName]; duplicate {
			return nil, false, fmt.Errorf("%w: build input %q resolves to multiple versions", ErrAgentWorkflowResourceSourceConflict, selected.SourceName)
		}
		seen[selected.SourceName] = struct{}{}
		selected.ResourceVersionID = selected.ResourceConfigVersionID
		selected.SelectingBuildID = int64(buildID)
		if err := json.Unmarshal(versionJSON, &selected.Version); err != nil {
			return nil, false, err
		}
		if err := validateSourceBuildVersionDigest(selected.VersionDigest, selected.Version); err != nil {
			return nil, false, err
		}
		selections = append(selections, selected)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(selections) == 0 {
		return nil, false, fmt.Errorf("%w: successful admit build has no persisted inputs", ErrAgentWorkflowResourceSourceConflict)
	}
	return selections, true, nil
}

func (store *workflowResourceSourceBuildStore) validateManualSourceBuild(ctx context.Context, teamID, pipelineID, buildID int) error {
	var matched bool
	err := store.conn.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM builds build JOIN jobs job ON job.id=build.job_id
		JOIN agent_workflow_resource_source_admissions admission ON admission.selecting_build_id=build.id
		WHERE build.id=$1 AND build.team_id=$2 AND build.pipeline_id=$3 AND job.pipeline_id=$3 AND job.name='admit' AND build.manually_triggered
		  AND admission.team_id=$2 AND admission.source_pipeline_id=$3 AND admission.mode='manual'
	)`, buildID, teamID, pipelineID).Scan(&matched)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("%w: build is not a manual source admission build", ErrAgentWorkflowResourceSourceConflict)
	}
	return nil
}

func validateWorkflowResourceSourceSelectingBuild(ctx context.Context, queryer snapshotQueryer, teamID, pipelineID int, buildID int64) error {
	var matched bool
	err := queryer.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM builds build JOIN jobs job ON job.id=build.job_id JOIN pipelines pipeline ON pipeline.id=build.pipeline_id
		WHERE build.id=$1 AND build.team_id=$2 AND build.pipeline_id=$3 AND job.pipeline_id=$3 AND job.name='admit' AND NOT pipeline.template
	)`, buildID, teamID, pipelineID).Scan(&matched)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("%w: selecting build is not the registered pipeline's admit job", ErrAgentWorkflowResourceSourceConflict)
	}
	return nil
}

func workflowResourceSourceAdmitJob(conn DbConn, lockFactory lock.LockFactory, tx Tx, pipelineID int) (*job, bool, error) {
	row := jobsQuery.Where(sq.Eq{"j.pipeline_id": pipelineID, "j.name": "admit", "j.active": true}).RunWith(tx).QueryRow()
	admit := newEmptyJob(conn, lockFactory)
	if err := scanJob(admit, row); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return admit, true, nil
}

func sourceBuildResourceTypes(conn DbConn, lockFactory lock.LockFactory, pipelineID int) (ResourceTypes, error) {
	rows, err := resourceTypesQuery.Where(sq.Eq{"r.pipeline_id": pipelineID}).OrderBy("r.name").RunWith(conn).Query()
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	values := ResourceTypes{}
	for rows.Next() {
		resourceType := newEmptyResourceType(conn, lockFactory)
		if err := scanResourceType(resourceType, rows); err != nil {
			return nil, err
		}
		values = append(values, resourceType)
	}
	return values, rows.Err()
}

func sourceBuildUnpinnedInputResourceIDs(ctx context.Context, conn DbConn, pipelineID int) (map[int]struct{}, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT DISTINCT resource.id FROM resources resource JOIN job_inputs input ON input.resource_id=resource.id JOIN jobs job ON job.id=input.job_id
		WHERE job.pipeline_id=$1 AND job.name='admit' AND resource.pipeline_id=$1
		  AND NOT EXISTS (SELECT FROM resource_pins pin WHERE pin.resource_id=resource.id) ORDER BY resource.id`, pipelineID)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	ids := map[int]struct{}{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = struct{}{}
	}
	return ids, rows.Err()
}

func validateSourceBuildVersionDigest(digest string, version atc.Version) error {
	versionJSON, err := json.Marshal(version)
	if err != nil {
		return err
	}
	switch len(digest) {
	case md5.Size * 2:
		sum := md5.Sum(versionJSON) // #nosec G401 -- validates legacy stored digest.
		if digest != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("%w: selected version JSON does not match digest", ErrAgentWorkflowResourceSourceConflict)
		}
	case sha256.Size * 2:
		sum := sha256.Sum256(versionJSON)
		if digest != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("%w: selected version JSON does not match digest", ErrAgentWorkflowResourceSourceConflict)
		}
	default:
		return fmt.Errorf("%w: selected version digest is malformed", ErrAgentWorkflowResourceSourceConflict)
	}
	return nil
}
