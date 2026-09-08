package atc

import (
	"encoding/json"
	"time"
)

type RunStatus string

const (
	RunStatusRunning   RunStatus = "running"
	RunStatusSucceeded RunStatus = "succeeded"
	RunStatusFailed    RunStatus = "failed"
	RunStatusErrored   RunStatus = "errored"
	RunStatusAborted   RunStatus = "aborted"
)

// PipelineRunCompletedChannel is the Postgres NOTIFY channel a run's
// transition to a terminal status is announced on. It exists so a walker can
// react to completion the instant it happens instead of discovering it on its
// next sweep.
//
// It is a wake-up, not a message. The channel carries no payload, the
// in-process bus coalesces every notification for a channel into one signal
// per waiting listener, and NotifySignal drops a signal outright when a
// listener's buffer is already full. A missed wake-up is therefore normal and
// unremarkable. Polling remains the source of truth: anything listening here
// must still do a full scan on its own interval and must be correct with the
// notification never arriving at all.
//
// It is deliberately separate from ComponentReclaimerPipelineRuns. That
// channel wakes one specific component on its own schedule; this one names the
// event, so a second consumer can listen without being mistaken for the
// reclaimer.
const PipelineRunCompletedChannel = "pipeline_run_completed"

type PipelineRun struct {
	ID                 int                 `json:"id"`
	TemplatePipelineID int                 `json:"template_pipeline_id"`
	Number             int                 `json:"number"`
	Params             *Params             `json:"params,omitempty"`
	Status             RunStatus           `json:"status"`
	CreatedBy          string              `json:"created_by"`
	CreatedAt          time.Time           `json:"created_at"`
	CompletedAt        *time.Time          `json:"completed_at,omitempty"`
	ReclaimRetryAfter  *time.Time          `json:"reclaim_retry_after,omitempty"`
	ConfigHash         *string             `json:"config_hash,omitempty"`
	Reclaimed          bool                `json:"reclaimed"`
	InstanceRef        *PipelineIdentifier `json:"instance_ref,omitempty"`
}

type pipelineRunAlias PipelineRun

type pipelineRunWire struct {
	*pipelineRunAlias
	CreatedAt         int64  `json:"created_at"`
	CompletedAt       *int64 `json:"completed_at,omitempty"`
	ReclaimRetryAfter *int64 `json:"reclaim_retry_after,omitempty"`
}

// MarshalJSON keeps the public run timestamps compatible with Fly's time.Time
// fields while retaining the Unix-second wire contract consumed by the web UI.
func (run PipelineRun) MarshalJSON() ([]byte, error) {
	wire := pipelineRunWire{
		pipelineRunAlias: (*pipelineRunAlias)(&run),
		CreatedAt:        run.CreatedAt.Unix(),
	}
	if run.CompletedAt != nil {
		completedAt := run.CompletedAt.Unix()
		wire.CompletedAt = &completedAt
	}
	if run.ReclaimRetryAfter != nil {
		reclaimRetryAfter := run.ReclaimRetryAfter.Unix()
		wire.ReclaimRetryAfter = &reclaimRetryAfter
	}

	return json.Marshal(wire)
}

// UnmarshalJSON restores Unix-second wire timestamps to the public time.Time fields.
func (run *PipelineRun) UnmarshalJSON(data []byte) error {
	decoded := pipelineRunAlias{}
	wire := pipelineRunWire{pipelineRunAlias: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	decoded.CreatedAt = time.Unix(wire.CreatedAt, 0).UTC()
	decoded.CompletedAt = pipelineRunTime(wire.CompletedAt)
	decoded.ReclaimRetryAfter = pipelineRunTime(wire.ReclaimRetryAfter)
	*run = PipelineRun(decoded)
	return nil
}

func pipelineRunTime(seconds *int64) *time.Time {
	if seconds == nil {
		return nil
	}

	timestamp := time.Unix(*seconds, 0).UTC()
	return &timestamp
}

type CreatePipelineRunRequest struct {
	Vars map[string]any `json:"vars"`
}
