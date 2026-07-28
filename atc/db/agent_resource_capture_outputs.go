package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/concourse/concourse/agent/snapshot"
)

// ResourceCaptureOutput is server-derived finalization work. It contains no
// caller authority: the background finalizer supplies its own fixed actor.
type ResourceCaptureOutput struct {
	TeamID        int
	TeamName      string
	PipelineRunID int64
	OperationKey  string
	OutputPort    string
	ExpectedType  snapshot.TypeRef
}

// ListPendingResourceCaptureOutputs returns only exact, available outputs of
// succeeded server-owned resource-capture runs that do not yet carry a pin for
// finalizerActor. The same ownership and production evidence used by
// FindResourceCaptureOutput is re-established here; arbitrary build metadata
// cannot enqueue finalization work.
func (factory *agentSnapshotsFactory) ListPendingResourceCaptureOutputs(
	ctx context.Context,
	finalizerActor string,
	limit int,
) ([]ResourceCaptureOutput, error) {
	if ctx == nil || strings.TrimSpace(finalizerActor) == "" || finalizerActor != strings.TrimSpace(finalizerActor) {
		return nil, fmt.Errorf("db: resource capture finalizer actor is invalid")
	}
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("db: resource capture finalizer limit must be between 1 and 1000")
	}
	rows, err := factory.conn.QueryContext(ctx, `
		SELECT template.team_id, team.name, run.id,
		       production.source_metadata ->> 'operation_key', production.output_port,
		       s.type_name, s.type_version
		FROM pipeline_runs run
		JOIN pipelines template ON template.id = run.template_pipeline_id
		JOIN agent_workflow_run_templates owned ON owned.pipeline_id = template.id
		JOIN teams team ON team.id = template.team_id
		JOIN pipelines instance ON instance.id = run.instance_pipeline_id
		JOIN builds build ON build.pipeline_id = instance.id AND build.team_id = template.team_id
		JOIN agent_snapshot_productions production
		  ON production.build_id = build.id
		 AND production.occurrence_kind = 'build'
		 AND production.team_id = template.team_id
		JOIN agent_snapshots s ON s.id = production.snapshot_id AND s.team_id = template.team_id
		WHERE run.status = 'succeeded'
		  AND template.template = true
		  AND template.instance_vars IS NULL
		  AND template.archived = false
		  AND instance.name = template.name
		  AND instance.team_id = template.team_id
		  AND instance.instance_vars IS NOT NULL
		  AND production.output_port = 'snapshot'
		  AND production.step_kind = 'task'
		  AND production.step_name = 'seal-snapshot'
		  AND production.source_metadata ->> 'adapter' = 'resource-version'
		  AND production.source_metadata ->> 'operation_key' ~ '^[0-9a-f]{64}$'
		  AND template.name = 'agent-resource-capture-' || left(production.source_metadata ->> 'operation_key', 24)
		  AND production.source_metadata ->> 'snapshot_type' = s.type_name || '/v' || s.type_version::text
		  AND s.content_state = 'available'
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_workflow_runs workflow_run
		    WHERE workflow_run.template_pipeline_id = template.id
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM agent_snapshot_retention_claims claim
		    WHERE claim.snapshot_id = s.id
		      AND claim.team_id = template.team_id
		      AND claim.class = 'pin'
		      AND claim.actor = $1
		  )
		ORDER BY run.id, production.id
		LIMIT $2
	`, finalizerActor, limit)
	if err != nil {
		return nil, err
	}
	defer Close(rows)

	outputs := make([]ResourceCaptureOutput, 0)
	for rows.Next() {
		var output ResourceCaptureOutput
		var typeName string
		var typeVersion int
		if err := rows.Scan(
			&output.TeamID, &output.TeamName, &output.PipelineRunID,
			&output.OperationKey, &output.OutputPort, &typeName, &typeVersion,
		); err != nil {
			return nil, err
		}
		decoded, err := hex.DecodeString(output.OperationKey)
		if err != nil || len(decoded) != sha256.Size || output.OperationKey != strings.ToLower(output.OperationKey) {
			return nil, fmt.Errorf("db: pending resource capture has an invalid operation key")
		}
		output.ExpectedType, err = joinSnapshotType(typeName, typeVersion)
		if err != nil {
			return nil, err
		}
		if output.TeamID <= 0 || strings.TrimSpace(output.TeamName) == "" || output.PipelineRunID <= 0 || output.OutputPort != "snapshot" {
			return nil, fmt.Errorf("db: pending resource capture has invalid durable identity")
		}
		outputs = append(outputs, output)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return outputs, nil
}
