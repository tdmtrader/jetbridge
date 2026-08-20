package atc

import "time"

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusErrored   RunStatus = "errored"
	RunStatusAborted   RunStatus = "aborted"
)

type PipelineRun struct {
	ID                 int        `json:"id"`
	TemplatePipelineID int        `json:"template_pipeline_id"`
	Number             int        `json:"number"`
	Params             Params     `json:"params"`
	Status             RunStatus  `json:"status"`
	CreatedBy          string     `json:"created_by"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ReclaimRetryAfter  *time.Time `json:"reclaim_retry_after,omitempty"`
	ConfigHash         string     `json:"config_hash"`
}
