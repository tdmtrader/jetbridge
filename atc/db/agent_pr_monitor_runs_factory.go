package db

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/concourse/concourse/agent/publisher"
	"github.com/concourse/concourse/agent/pullrequest"
	"github.com/concourse/concourse/agent/snapshot"
)

const maxExpectedAgentPRMonitorPublicationProofs = 3

// AgentPRMonitorRunsFactory is the read-only, team-scoped projection used to
// classify one attached monitor run. Publication records are read through the
// existing publication occurrence decoder and verifier.
type AgentPRMonitorRunsFactory interface {
	pullrequest.MonitorRunEvidenceReader
}

func NewAgentPRMonitorRunsFactory(
	conn DbConn,
) AgentPRMonitorRunsFactory {
	return &agentPRMonitorRunsFactory{conn: conn}
}

type agentPRMonitorRunsFactory struct {
	conn DbConn
}

func (factory *agentPRMonitorRunsFactory) ReadMonitorRunEvidence(
	ctx context.Context,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
) (pullrequest.MonitorRunEvidence, bool, error) {
	if ctx == nil || teamID <= 0 || workflowRunID.Validate() != nil {
		return pullrequest.MonitorRunEvidence{}, false, fmt.Errorf(
			"db: PR monitor run evidence requires context, team, and workflow run",
		)
	}
	if err := ctx.Err(); err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	tx, err := factory.conn.BeginTx(
		ctx,
		&sql.TxOptions{
			Isolation: sql.LevelRepeatableRead,
			ReadOnly:  true,
		},
	)
	if err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	defer Rollback(tx)

	evidence, found, err := readAgentPRMonitorRunCore(
		ctx, tx, teamID, workflowRunID,
	)
	if err != nil || !found {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	publications, overflow, err := readAgentPRMonitorRunPublications(
		ctx, tx, teamID, workflowRunID,
	)
	if err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	evidence.Publications = publications
	evidence.PublicationProofOverflow = overflow
	if err := tx.Commit(); err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	return evidence, true, nil
}

func readAgentPRMonitorRunCore(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
) (pullrequest.MonitorRunEvidence, bool, error) {
	var (
		evidence               pullrequest.MonitorRunEvidence
		runStatus              string
		provider               string
		activeBaseRevision     int64
		currentBindingRevision int64
		activeObservationID    int64
		activeCursor           []byte
		selectedVersion        []byte
		observationTypeName    string
		observationTypeVersion int
		observationDigest      string
	)
	err := queryer.QueryRowContext(ctx, `
		SELECT run.status,
		       binding.id,binding.revision,binding.active_base_revision,
		       binding.active_action_digest,binding.active_reservation_token,
		       binding.active_observation_snapshot_id,binding.active_cursor,
		       binding.active_source_sha,binding.active_target_sha,
		       binding.provider,binding.repository,binding.external_id,
		       binding.url,binding.source_ref,binding.target_ref,
		       binding.destination,binding.approval_policy_version,
		       selected.version,
		       observation.type_name,observation.type_version,
		       observation.digest
		FROM agent_workflow_runs run
		JOIN agent_pr_bindings binding
		  ON binding.team_id=run.team_id
		 AND binding.active_workflow_run_id=run.id
		JOIN agent_workflow_resource_source_admissions admission
		  ON admission.id=run.resource_source_admission_id
		 AND admission.team_id=run.team_id
		 AND admission.workflow_definition_id=run.workflow_definition_id
		 AND admission.status='ready'
		JOIN agent_workflow_resource_source_pipelines registry
		  ON registry.pipeline_id=admission.source_pipeline_id
		 AND registry.team_id=run.team_id
		 AND registry.workflow_definition_id=run.workflow_definition_id
		 AND registry.pr_binding_id=binding.id
		 AND registry.workflow_version=run.workflow_version
		JOIN agent_workflow_resource_source_bindings selected
		  ON selected.admission_id=admission.id
		 AND selected.source_pipeline_id=admission.source_pipeline_id
		 AND selected.source_name='pull-request'
		 AND selected.snapshot_id=binding.active_observation_snapshot_id
		 AND selected.snapshot_type_name='pull-request'
		 AND selected.snapshot_type_version=1
		JOIN agent_snapshots observation
		  ON observation.id=selected.snapshot_id
		 AND observation.team_id=run.team_id
		WHERE run.id=$1
		  AND run.team_id=$2
		  AND run.definition_kind='workflow'
		  AND run.origin_kind='pr-monitor'
		  AND run.origin_reference=binding.id::text
		  AND run.workflow_definition_id=
		      binding.monitor_workflow_definition_id
		  AND run.workflow_version=binding.monitor_workflow_version
	`, int64(workflowRunID), teamID).Scan(
		&runStatus,
		&evidence.BindingID, &currentBindingRevision,
		&activeBaseRevision,
		&evidence.ActionDigest, &evidence.ReservationToken,
		&activeObservationID, &activeCursor,
		&evidence.SourceSHA, &evidence.TargetSHA,
		&provider, &evidence.Locator.Repository,
		&evidence.Locator.ExternalID,
		&evidence.URL,
		&evidence.SourceRef, &evidence.TargetRef,
		&evidence.Destination, &evidence.ApprovalPolicyVersion,
		&selectedVersion,
		&observationTypeName, &observationTypeVersion,
		&observationDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return pullrequest.MonitorRunEvidence{}, false, nil
	}
	if err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	if activeBaseRevision <= 0 ||
		currentBindingRevision <= activeBaseRevision {
		return pullrequest.MonitorRunEvidence{}, false, fmt.Errorf(
			"db: PR monitor run binding revision is invalid",
		)
	}
	observationType, err := joinSnapshotType(
		observationTypeName, observationTypeVersion,
	)
	if err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	cursor, err := decodeAgentPRCursor(activeCursor)
	if err != nil {
		return pullrequest.MonitorRunEvidence{}, false, fmt.Errorf(
			"db: decode PR monitor run cursor: %w", err,
		)
	}
	evidence.TeamID = teamID
	evidence.BindingRevision = activeBaseRevision + 1
	evidence.WorkflowRunID = workflowRunID
	evidence.Observation = snapshot.SnapshotRef{
		ID:   snapshot.SnapshotID(activeObservationID),
		Type: observationType, Digest: snapshot.Digest(observationDigest),
	}
	evidence.Cursor = cursor
	evidence.Locator.Provider = pullrequest.Provider(provider)
	evidence.RunStatus = pullrequest.MonitorEvidenceRunStatus(runStatus)
	actionKind, err := decodeAgentPRMonitorSelectedVersion(
		selectedVersion, evidence, activeBaseRevision,
	)
	if err != nil {
		return pullrequest.MonitorRunEvidence{}, false, err
	}
	evidence.ActionKind = actionKind
	return evidence, true, nil
}

func decodeAgentPRMonitorSelectedVersion(
	raw []byte,
	evidence pullrequest.MonitorRunEvidence,
	activeBaseRevision int64,
) (pullrequest.ActionKind, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var version map[string]string
	if err := decoder.Decode(&version); err != nil {
		return "", fmt.Errorf(
			"db: decode PR monitor selected version: %w", err,
		)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf(
			"db: PR monitor selected version has trailing content",
		)
	}
	if len(version) != 8 ||
		version["provider"] != string(evidence.Locator.Provider) ||
		version["external_id"] != evidence.Locator.ExternalID ||
		version["source_sha"] != evidence.SourceSHA ||
		version["target_sha"] != evidence.TargetSHA ||
		version["action_digest"] != evidence.ActionDigest ||
		version["cursor"] != string(evidence.Cursor) ||
		version["binding_revision"] != strconv.FormatInt(
			activeBaseRevision, 10,
		) {
		return "", fmt.Errorf(
			"db: PR monitor selected version is not exact",
		)
	}
	kind := pullrequest.ActionKind(version["action_kind"])
	switch kind {
	case pullrequest.ActionReviewBatch, pullrequest.ActionConflict,
		pullrequest.ActionFreshness, pullrequest.ActionCompleted,
		pullrequest.ActionAbandoned:
		return kind, nil
	default:
		return "", fmt.Errorf(
			"db: PR monitor selected version action is invalid",
		)
	}
}

func readAgentPRMonitorRunPublications(
	ctx context.Context,
	queryer snapshotQueryer,
	teamID int,
	workflowRunID snapshot.WorkflowRunID,
) ([]publisher.Publication, bool, error) {
	rows, err := queryer.QueryContext(ctx, `
		SELECT `+agentPublicationColumns+agentPublicationFrom+`
		WHERE occurrence.team_id=$1
		  AND occurrence.workflow_run_id=$2
		ORDER BY occurrence.id
		LIMIT $3
	`, teamID, int64(workflowRunID),
		maxExpectedAgentPRMonitorPublicationProofs+1)
	if err != nil {
		return nil, false, err
	}
	publications := make(
		[]publisher.Publication,
		0,
		maxExpectedAgentPRMonitorPublicationProofs+1,
	)
	for rows.Next() {
		record, err := scanAgentPublication(rows)
		if err != nil {
			Close(rows)
			return nil, false, err
		}
		publications = append(
			publications, record.publication.Clone(),
		)
	}
	if err := rows.Err(); err != nil {
		Close(rows)
		return nil, false, err
	}
	Close(rows)
	for _, publication := range publications {
		if err := validateStoredPRPublication(
			ctx, queryer, publication,
		); err != nil {
			return nil, false, err
		}
	}
	return publications,
		len(publications) > maxExpectedAgentPRMonitorPublicationProofs,
		nil
}

var _ pullrequest.MonitorRunEvidenceReader = (*agentPRMonitorRunsFactory)(nil)
