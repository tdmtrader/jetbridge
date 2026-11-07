package atc

import (
	"encoding/json"
	"errors"
	"time"
)

// ErrPipelineTemplateHasRunHistory refuses an ordinary destroy of a template
// that has ever had a Run. The Runs' durable history outlives their payloads,
// so an ordinary destroy would orphan it; destroying the template together
// with its Runs is a separate, explicit request
// (DestroyPipelineQueryPurgeRunHistory). It lives here and not only in the
// store so a client can recognise the refusal by the sentence it carries.
var ErrPipelineTemplateHasRunHistory = errors.New("template with durable run history cannot be destroyed")

// DestroyPipelineQueryPurgeRunHistory is the query parameter that asks
// DELETE /api/v1/teams/:team_name/pipelines/:pipeline_name to destroy a
// template together with all of its Runs and their durable history. Its only
// accepted values are "true" and "false".
const DestroyPipelineQueryPurgeRunHistory = "purge_run_history"

// DestroyPipelineResponse answers a destroy that asked to purge the run
// history: how many Runs went with the template.
type DestroyPipelineResponse struct {
	PurgedRuns int `json:"purged_runs"`
}

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
	Captures []RunCaptureProgress `json:"captures,omitempty"`
	// Terminal is included only in authorized detail responses, not listings.
	Terminal     *RunTerminalResult      `json:"terminal,omitempty"`
	CanCancel    bool                    `json:"can_cancel,omitempty"`
	Cancellation *RunCancellationRequest `json:"cancellation,omitempty"`
	// AdmissionOutcome is set only on a v2 create response: created for a
	// newly committed Run, replayed for an invocation that had already
	// committed. A replay also carries the Idempotency-Replayed header.
	AdmissionOutcome  RunAdmissionOutcome `json:"admission_outcome,omitempty"`
	ID                int                 `json:"id"`
	TemplateID        int                 `json:"template_pipeline_id"`
	Number            int                 `json:"number"`
	Params            *Params             `json:"params,omitempty"`
	Status            RunStatus           `json:"status"`
	CreatedBy         string              `json:"created_by"`
	CreatedAt         time.Time           `json:"created_at"`
	CompletedAt       *time.Time          `json:"completed_at,omitempty"`
	ReclaimRetryAfter *time.Time          `json:"reclaim_retry_after,omitempty"`
	ConfigHash        *string             `json:"config_hash,omitempty"`
	Reclaimed         bool                `json:"reclaimed"`
	InstanceRef       *PipelineIdentifier `json:"instance_ref,omitempty"`
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

// RunAdmissionOutcome says whether a v2 create committed a new Run or replayed
// the Run its invocation had already committed.
type RunAdmissionOutcome string

const (
	RunAdmissionCreated  RunAdmissionOutcome = "created"
	RunAdmissionReplayed RunAdmissionOutcome = "replayed"
)

// IdempotencyReplayedHeader is set to "true" on a v2 create response that
// replayed an already-committed invocation, and is absent otherwise.
const IdempotencyReplayedHeader = "Idempotency-Replayed"

// ValidRunInvocationToken reports whether value is 1 through 128 bytes of the
// invocation alphabet: A-Z, a-z, 0-9, dot, underscore, tilde and hyphen. The
// invocation key uses this alphabet.
func ValidRunInvocationToken(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, c := range []byte(value) {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// CreatePipelineRunV2Request carries invocation intent. The principal and
// identity comes from the server; input bearers are never retained.
type CreatePipelineRunV2Request struct {
	InvocationKey string                    `json:"invocation_key"`
	Vars          RunParams                 `json:"vars,omitempty"`
	Inputs        map[string]RunInputSource `json:"inputs,omitempty"`
}
