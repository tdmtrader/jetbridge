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
	ID() int
	TemplatePipelineID() int
	Number() int
	Params() atc.Params
	Status() atc.RunStatus
	CreatedBy() string
	CreatedAt() time.Time
	UpdatedAt() time.Time
	ReclaimRetryAfter() *time.Time
	ConfigHash() string
	InstancePipelineID() (int, bool)
}

type pipelineRun struct {
	id                 int
	templatePipelineID int
	number             int
	params             atc.Params
	status             atc.RunStatus
	createdBy          string
	createdAt          time.Time
	updatedAt          time.Time
	reclaimRetryAfter  *time.Time
	configHash         string
	instancePipelineID int
}

func (r *pipelineRun) ID() int                 { return r.id }
func (r *pipelineRun) TemplatePipelineID() int { return r.templatePipelineID }
func (r *pipelineRun) Number() int             { return r.number }
func (r *pipelineRun) Params() atc.Params      { return r.params }
func (r *pipelineRun) Status() atc.RunStatus   { return r.status }
func (r *pipelineRun) CreatedBy() string       { return r.createdBy }
func (r *pipelineRun) CreatedAt() time.Time    { return r.createdAt }
func (r *pipelineRun) UpdatedAt() time.Time    { return r.updatedAt }
func (r *pipelineRun) ConfigHash() string      { return r.configHash }
func (r *pipelineRun) InstancePipelineID() (int, bool) {
	return r.instancePipelineID, r.instancePipelineID != 0
}
func (r *pipelineRun) ReclaimRetryAfter() *time.Time { return r.reclaimRetryAfter }

var pipelineRunsQuery = psql.Select(
	"r.id", "r.template_pipeline_id", "r.number", "r.params", "r.status", "r.created_by",
	"r.created_at", "r.updated_at", "r.reclaim_retry_after", "r.config_hash", "child.id",
).From("pipeline_runs r").
	LeftJoin("pipelines child ON child.pipeline_run_id = r.id")

func scanPipelineRun(run *pipelineRun, row scannable) error {
	var params sql.NullString
	var reclaimRetryAfter sql.NullTime
	var instancePipelineID sql.NullInt64
	if err := row.Scan(&run.id, &run.templatePipelineID, &run.number, &params, &run.status, &run.createdBy,
		&run.createdAt, &run.updatedAt, &reclaimRetryAfter, &run.configHash, &instancePipelineID); err != nil {
		return err
	}
	if params.Valid && json.Unmarshal([]byte(params.String), &run.params) != nil {
		return json.Unmarshal([]byte(params.String), &run.params)
	}
	if reclaimRetryAfter.Valid {
		run.runReclaimRetryAfter(reclaimRetryAfter.Time)
	}
	run.instancePipelineID = int(instancePipelineID.Int64)
	return nil
}

func (r *pipelineRun) runReclaimRetryAfter(value time.Time) { r.reclaimRetryAfter = &value }
