package db

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
)

const agentSnapshotColumns = `
	id, type_name, type_version, digest, byte_size, file_count,
	representation, intrinsic_metadata, content_state, created_at`

func splitSnapshotType(typeRef snapshot.TypeRef) (string, int, error) {
	if err := typeRef.Validate(); err != nil {
		return "", 0, err
	}
	separator := strings.LastIndex(string(typeRef), "/v")
	if separator < 1 {
		return "", 0, fmt.Errorf("db: invalid snapshot type %q", typeRef)
	}
	version, err := strconv.Atoi(string(typeRef)[separator+2:])
	if err != nil || version <= 0 {
		return "", 0, fmt.Errorf("db: invalid snapshot type %q", typeRef)
	}
	return string(typeRef)[:separator], version, nil
}

func joinSnapshotType(name string, version int) (snapshot.TypeRef, error) {
	return snapshot.ParseTypeRef(fmt.Sprintf("%s/v%d", name, version))
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}

func cloneJSON(value []byte) json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func semanticJSONEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	decode := func(raw json.RawMessage) (any, error) {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		var value any
		err := decoder.Decode(&value)
		return value, err
	}
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && reflect.DeepEqual(leftValue, rightValue)
}

func scanAgentSnapshot(row scannable) (snapshot.Snapshot, error) {
	var (
		id                int64
		typeName          string
		typeVersion       int
		digest            string
		byteSize          int64
		fileCount         int64
		representation    string
		intrinsicMetadata []byte
		contentState      string
		createdAt         time.Time
	)
	err := row.Scan(
		&id, &typeName, &typeVersion, &digest, &byteSize, &fileCount,
		&representation, &intrinsicMetadata, &contentState, &createdAt,
	)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	typeRef, err := joinSnapshotType(typeName, typeVersion)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	value := snapshot.Snapshot{
		ID: snapshot.SnapshotID(id), Type: typeRef, Digest: snapshot.Digest(digest),
		ByteSize: byteSize, FileCount: fileCount, Representation: representation,
		IntrinsicMetadata: cloneJSON(intrinsicMetadata),
		ContentState:      snapshot.ContentState(contentState), CreatedAt: createdAt,
	}
	if err := value.Validate(); err != nil {
		return snapshot.Snapshot{}, fmt.Errorf("db: invalid persisted snapshot: %w", err)
	}
	return value, nil
}

func scanStagedUpload(row scannable) (snapshot.StagedUpload, error) {
	var staged snapshot.StagedUpload
	var digest string
	err := row.Scan(
		&staged.ID, &digest, &staged.TeamID, &staged.Attempt,
		&staged.LeaseExpiresAt, &staged.CreatedAt,
	)
	if err != nil {
		return snapshot.StagedUpload{}, err
	}
	staged.Digest = snapshot.Digest(digest)
	if err := staged.Validate(); err != nil {
		return snapshot.StagedUpload{}, fmt.Errorf("db: invalid persisted staged upload: %w", err)
	}
	return staged, nil
}

func scanLocation(row scannable) (snapshot.Location, error) {
	var location snapshot.Location
	var digest string
	err := row.Scan(&digest, &location.Driver, &location.Key, &location.Node)
	if err != nil {
		return snapshot.Location{}, err
	}
	location.Digest = snapshot.Digest(digest)
	if err := location.Validate(); err != nil {
		return snapshot.Location{}, fmt.Errorf("db: invalid persisted snapshot location: %w", err)
	}
	return location, nil
}

func scanRetentionClaim(row scannable) (snapshot.RetentionClaim, error) {
	var (
		claim     snapshot.RetentionClaim
		class     string
		expiresAt sql.NullTime
	)
	err := row.Scan(
		&claim.ID, (*int64)(&claim.SnapshotID), &class, &expiresAt,
		&claim.Actor, &claim.Reason, &claim.CreatedAt,
	)
	if err != nil {
		return snapshot.RetentionClaim{}, err
	}
	claim.Class = snapshot.RetentionClass(class)
	if expiresAt.Valid {
		claim.ExpiresAt = &expiresAt.Time
	}
	if err := claim.Validate(); err != nil {
		return snapshot.RetentionClaim{}, fmt.Errorf("db: invalid persisted retention claim: %w", err)
	}
	return claim, nil
}

func scanProductionDetail(row scannable) (snapshot.ProductionDetail, error) {
	var (
		production                          snapshot.ProductionDetail
		kind                                string
		buildID                             sql.NullInt64
		planID, attempt, stepKind, stepName sql.NullString
		outputPort                          sql.NullString
		workflowDefinitionID, workflowRunID sql.NullInt64
		sourceMetadata                      []byte
	)
	err := row.Scan(
		&production.ID, &kind, &production.CreatedBy,
		&buildID, &planID, &attempt, &stepKind, &stepName, &outputPort,
		&workflowDefinitionID, &workflowRunID, &sourceMetadata, &production.CreatedAt,
	)
	if err != nil {
		return snapshot.ProductionDetail{}, err
	}
	production.Kind = snapshot.ProductionKind(kind)
	production.OutputPort = outputPort.String
	production.SourceMetadata = cloneJSON(sourceMetadata)
	production.Inputs = make([]snapshot.ProductionInput, 0)
	if production.Kind == snapshot.ProductionKindBuild {
		build := snapshot.BuildOccurrence{
			BuildID: int(buildID.Int64), PlanID: planID.String, Attempt: attempt.String,
			StepKind: stepKind.String, StepName: stepName.String,
		}
		if workflowDefinitionID.Valid {
			value := int(workflowDefinitionID.Int64)
			build.WorkflowDefinitionID = &value
		}
		if workflowRunID.Valid {
			value := snapshot.WorkflowRunID(workflowRunID.Int64)
			build.WorkflowRunID = &value
		}
		production.Build = &build
	}
	if err := production.Validate(); err != nil {
		return snapshot.ProductionDetail{}, fmt.Errorf("db: invalid persisted production detail: %w", err)
	}
	return production, nil
}

func scanProductionSummary(row scannable) (snapshot.ProductionSummary, error) {
	var (
		production                          snapshot.ProductionSummary
		kind                                string
		buildID                             sql.NullInt64
		planID, attempt, stepKind, stepName sql.NullString
		outputPort                          sql.NullString
		workflowDefinitionID, workflowRunID sql.NullInt64
		snapshotID                          int64
		typeName, digest                    string
		typeVersion                         int
	)
	err := row.Scan(
		&production.ID, &kind, &production.CreatedBy,
		&buildID, &planID, &attempt, &stepKind, &stepName, &outputPort,
		&workflowDefinitionID, &workflowRunID, &production.CreatedAt,
		&snapshotID, &typeName, &typeVersion, &digest,
	)
	if err != nil {
		return snapshot.ProductionSummary{}, err
	}
	production.Kind = snapshot.ProductionKind(kind)
	production.OutputPort = outputPort.String
	typeRef, err := joinSnapshotType(typeName, typeVersion)
	if err != nil {
		return snapshot.ProductionSummary{}, err
	}
	production.Snapshot = snapshot.SnapshotRef{ID: snapshot.SnapshotID(snapshotID), Type: typeRef, Digest: snapshot.Digest(digest)}
	if production.Kind == snapshot.ProductionKindBuild {
		build := snapshot.BuildOccurrence{
			BuildID: int(buildID.Int64), PlanID: planID.String, Attempt: attempt.String,
			StepKind: stepKind.String, StepName: stepName.String,
		}
		if workflowDefinitionID.Valid {
			value := int(workflowDefinitionID.Int64)
			build.WorkflowDefinitionID = &value
		}
		if workflowRunID.Valid {
			value := snapshot.WorkflowRunID(workflowRunID.Int64)
			build.WorkflowRunID = &value
		}
		production.Build = &build
	}
	if err := production.Validate(); err != nil {
		return snapshot.ProductionSummary{}, fmt.Errorf("db: invalid persisted production summary: %w", err)
	}
	return production, nil
}

func optionalInt64(value *snapshot.WorkflowRunID) any {
	if value == nil {
		return nil
	}
	return int64(*value)
}

func optionalInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}
