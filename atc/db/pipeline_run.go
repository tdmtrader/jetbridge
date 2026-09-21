package db

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/concourse/concourse/atc"
)

// PipelineRun is the durable header that owns one numbered payload pipeline.
// Lifecycle operations intentionally live with the run service, not this model.
type PipelineRun interface {
	ContractVersion() atc.RunContractVersion
	CancellationRequested() bool
	CancellationRequest() *atc.RunCancellationRequest
	ActivationEpoch() int64
	ID() int
	TemplatePipelineID() int
	Number() int
	Params() atc.Params
	Status() atc.RunStatus
	CreatedBy() string
	CreatedAt() time.Time
	CompletedAt() *time.Time
	ReclaimRetryAfter() *time.Time
	ConfigHash() string
	InstancePipelineID() (int, bool)
}

type pipelineRun struct {
	cancellation       *atc.RunCancellationRequest
	contractVersion    atc.RunContractVersion
	activationEpoch    int64
	id                 int
	templatePipelineID int
	number             int
	params             atc.Params
	status             atc.RunStatus
	createdBy          string
	createdAt          time.Time
	completedAt        *time.Time
	reclaimRetryAfter  *time.Time
	configHash         string
	instancePipelineID int
}

func (r *pipelineRun) CancellationRequested() bool { return r.cancellation != nil }

func (r *pipelineRun) CancellationRequest() *atc.RunCancellationRequest { return r.cancellation }

func (r *pipelineRun) ContractVersion() atc.RunContractVersion { return r.contractVersion }
func (r *pipelineRun) ActivationEpoch() int64                  { return r.activationEpoch }

func (r *pipelineRun) ID() int                 { return r.id }
func (r *pipelineRun) TemplatePipelineID() int { return r.templatePipelineID }
func (r *pipelineRun) Number() int             { return r.number }
func (r *pipelineRun) Params() atc.Params      { return r.params }
func (r *pipelineRun) Status() atc.RunStatus   { return r.status }
func (r *pipelineRun) CreatedBy() string       { return r.createdBy }
func (r *pipelineRun) CreatedAt() time.Time    { return r.createdAt }
func (r *pipelineRun) CompletedAt() *time.Time { return r.completedAt }
func (r *pipelineRun) ConfigHash() string      { return r.configHash }
func (r *pipelineRun) InstancePipelineID() (int, bool) {
	return r.instancePipelineID, r.instancePipelineID != 0
}
func (r *pipelineRun) ReclaimRetryAfter() *time.Time { return r.reclaimRetryAfter }

var pipelineRunsQuery = psql.Select(
	"r.cancel_requested_at", "r.cancel_requested_by", "r.cancel_reason", "r.run_contract_version", "coalesce(r.activation_epoch, 0)", "r.id", "r.template_pipeline_id", "r.number", "r.params", "r.status", "r.created_by",
	"r.created_at", "r.completed_at", "r.reclaim_retry_after", "r.config_hash", "child.id",
).From("pipeline_runs r").
	LeftJoin("pipelines child ON child.pipeline_run_id = r.id")

func scanPipelineRun(run *pipelineRun, row scannable) error {
	var cancelAt sql.NullTime
	var cancelBy, cancelReason sql.NullString
	var params sql.NullString
	var completedAt sql.NullTime
	var reclaimRetryAfter sql.NullTime
	var instancePipelineID sql.NullInt64
	if err := row.Scan(&cancelAt, &cancelBy, &cancelReason, &run.contractVersion, &run.activationEpoch, &run.id, &run.templatePipelineID, &run.number, &params, &run.status, &run.createdBy,
		&run.createdAt, &completedAt, &reclaimRetryAfter, &run.configHash, &instancePipelineID); err != nil {
		return err
	}
	if cancelAt.Valid {
		run.cancellation = &atc.RunCancellationRequest{RequestedAt: cancelAt.Time.UTC(), RequestedBy: cancelBy.String}
		if cancelReason.Valid {
			run.cancellation.Reason = &cancelReason.String
		}
	}
	if params.Valid && json.Unmarshal([]byte(params.String), &run.params) != nil {
		return json.Unmarshal([]byte(params.String), &run.params)
	}
	if reclaimRetryAfter.Valid {
		run.runReclaimRetryAfter(reclaimRetryAfter.Time)
	}
	if completedAt.Valid {
		run.completedAt = &completedAt.Time
	}
	run.instancePipelineID = int(instancePipelineID.Int64)
	return nil
}

func (r *pipelineRun) runReclaimRetryAfter(value time.Time) { r.reclaimRetryAfter = &value }
