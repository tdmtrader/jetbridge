package db_test

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
)

// markPipelineWorkflowRunOwned installs only the authoritative durable link
// used by public manual-build guards. It deliberately does not create a
// selected build: the operation under test must be rejected before any build
// or scheduling mutation can occur.
func markPipelineWorkflowRunOwned(pipelineID, teamID int, teamName string) error {
	suffix := time.Now().UnixNano()
	var definitionID int
	if err := dbConn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'test', 3, 1)
		RETURNING id
	`, fmt.Sprintf("manual-build-guard-%d", suffix), fmt.Sprintf("hash-%d", suffix)).Scan(&definitionID); err != nil {
		return err
	}
	var pipelineRunID int
	if err := dbConn.QueryRow(`
		INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
		SELECT $1, $1, COALESCE(MAX(number), 0) + 1
		FROM pipeline_runs
		WHERE template_pipeline_id = $1
		RETURNING id
	`, pipelineID).Scan(&pipelineRunID); err != nil {
		return err
	}
	_, err := dbConn.Exec(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name,
			 workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash,
			 concrete_config, concrete_config_hash,
			 origin_kind, origin_reference, created_by, status,
			 pipeline_run_id, template_pipeline_id, instance_pipeline_id)
		VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6,
		        '{}', $7, '{}', $8, 'test', $9, 'test', 'admitting',
		        $10, $11, $11)
	`, teamID, teamName, definitionID, fmt.Sprintf("manual-build-guard-%d", suffix),
		fmt.Sprintf("definition-%d", suffix), fmt.Sprintf("request-%d", suffix),
		fmt.Sprintf("parameterized-%d", suffix), fmt.Sprintf("concrete-%d", suffix),
		fmt.Sprintf("pipeline:%d", pipelineID), pipelineRunID, pipelineID)
	return err
}

// markPipelineWorkflowResourceSourceOwned installs the registry authority used
// by public config and build guards. It intentionally does not rely on a
// lifecycle reconciler: the tests verify that ordinary entry points reject the
// row before they alter scheduler or pipeline state.
func markPipelineWorkflowResourceSourceOwned(pipelineID, teamID int, teamName string) error {
	suffix := time.Now().UnixNano()
	var definitionID, configVersion int
	if err := dbConn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'test', 3, 1)
		RETURNING id
	`, fmt.Sprintf("source-build-guard-%d", suffix), fmt.Sprintf("hash-%d", suffix)).Scan(&definitionID); err != nil {
		return err
	}
	if err := dbConn.QueryRow(`SELECT version FROM pipelines WHERE id=$1 AND team_id=$2`, pipelineID, teamID).Scan(&configVersion); err != nil {
		return err
	}
	declarations, err := json.Marshal([]db.ResourceSourceDeclaration{{
		SourceName: "repository-source", ResourceName: "repository", SnapshotType: "repository/v1",
	}})
	if err != nil {
		return err
	}
	_, err = dbConn.Exec(`
		INSERT INTO agent_workflow_resource_source_pipelines
			(pipeline_id,team_id,workflow_definition_id,workflow_name,workflow_version,pipeline_config_version,config_hash,source_declarations,state)
		VALUES ($1,$2,$3,$4,1,$5,$6,$7,'active')
	`, pipelineID, teamID, definitionID, fmt.Sprintf("source-build-guard-%d", suffix), configVersion,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", declarations)
	return err
}

func attachSelectedWorkflowRun(build db.Build, team db.Team, concrete atc.Config) (snapshot.WorkflowRunID, error) {
	suffix := time.Now().UnixNano()
	canonical, err := concrete.CanonicalJSON()
	if err != nil {
		return 0, err
	}
	if !json.Valid(canonical) {
		return 0, fmt.Errorf("test workflow concrete config is not valid JSON")
	}
	var definitionID int
	if err := dbConn.QueryRow(`
		INSERT INTO agent_workflow_definitions
			(name, version, content_hash, definition, created_by, schema_version, signature_version)
		VALUES ($1, 1, $2, 'schema_version: 3', 'test', 3, 1)
		RETURNING id
	`, fmt.Sprintf("build-hook-%d", suffix), fmt.Sprintf("hash-%d", suffix)).Scan(&definitionID); err != nil {
		return 0, err
	}
	var pipelineRunID int
	if err := dbConn.QueryRow(`
		INSERT INTO pipeline_runs (template_pipeline_id, instance_pipeline_id, number)
		SELECT $1, $1, COALESCE(MAX(number), 0) + 1
		FROM pipeline_runs
		WHERE template_pipeline_id = $1
		RETURNING id
	`, build.PipelineID()).Scan(&pipelineRunID); err != nil {
		return 0, err
	}
	var runID int64
	if err := dbConn.QueryRow(`
		INSERT INTO agent_workflow_runs
			(team_id, team_name, workflow_definition_id, workflow_name,
			 workflow_version, schema_version, signature_version,
			 definition_content_hash, idempotency_key,
			 parameterized_config, parameterized_config_hash,
			 concrete_config, concrete_config_hash,
			 origin_kind, origin_reference, created_by, status,
			 pipeline_run_id, template_pipeline_id, instance_pipeline_id,
			 planned_build_id)
		VALUES ($1, $2, $3, $4, 1, 3, 1, $5, $6,
		        $7, $8, $7, $9, 'test', $10, 'test', 'admitting',
		        $11, $12, $12, $13)
		RETURNING id
	`, team.ID(), team.Name(), definitionID, fmt.Sprintf("build-hook-%d", suffix),
		fmt.Sprintf("definition-%d", suffix), fmt.Sprintf("request-%d", suffix), canonical,
		fmt.Sprintf("parameterized-%d", suffix), fmt.Sprintf("concrete-%d", suffix),
		fmt.Sprintf("build:%d", build.ID()), pipelineRunID, build.PipelineID(), build.ID()).Scan(&runID); err != nil {
		return 0, err
	}
	return snapshot.WorkflowRunID(runID), nil
}
