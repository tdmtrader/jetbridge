package schema

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// WorkflowRunID is a positive 64-bit agent_workflow_runs primary key. It
// mirrors snapshot.WorkflowRunID's WIRE FORMAT byte-for-byte — a quoted decimal
// string, so a 64-bit id survives a JS client — without coupling this
// standalone contract module to the whole concourse module. The main module
// converts to/from
// snapshot.WorkflowRunID with a free int64 cast at its boundary.
type WorkflowRunID int64

// MarshalJSON emits the id as a quoted decimal string ("123"), identical to
// snapshot.WorkflowRunID. A non-positive id is invalid and errors.
func (id WorkflowRunID) MarshalJSON() ([]byte, error) {
	if id <= 0 {
		return nil, fmt.Errorf("workflow run ID must be positive")
	}
	return []byte(`"` + strconv.FormatInt(int64(id), 10) + `"`), nil
}

// UnmarshalJSON accepts both the marshaled quoted string ("123") and a bare
// JSON number (123), tolerating either producer. The value must be a positive
// decimal.
func (id *WorkflowRunID) UnmarshalJSON(data []byte) error {
	raw := strings.TrimSpace(string(data))
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		raw = raw[1 : len(raw)-1]
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fmt.Errorf("workflow run ID must be a positive decimal")
	}
	*id = WorkflowRunID(value)
	return nil
}

// String renders the canonical decimal, or "" for an invalid id.
func (id WorkflowRunID) String() string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(int64(id), 10)
}

// Usage captures token consumption from an LLM call. JSON field names match
// the claude CLI envelope.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// RunMetrics is one agent step's flight-recorder rollup — the row shape of
// agent_run_metrics, written in-process by the agent step
// (shared-contracts §2.4 / §1.8).
type RunMetrics struct {
	// WorkflowRunID is the durable schema-v3 workflow run this step ran in —
	// the metric's execution identity. Nil for an unbound CI invocation (a
	// build with no planned workflow run). It marshals as a quoted string so a
	// 64-bit id survives a JS client.
	WorkflowRunID *WorkflowRunID `json:"workflow_run_id,omitempty"`
	// FunctionID is the workflow function that produced the step. Empty for a
	// direct pipeline agent step.
	FunctionID string `json:"function_id,omitempty"`
	BuildID    int    `json:"build_id"`
	PlanID     string `json:"plan_id"`
	StepName   string `json:"step_name"`
	Status     string `json:"status"` // ok | failed | error | incomplete — the AGENT STEP exit status
	// BuildStatus is the status of the pipeline build the step ran in
	// (pending|started|succeeded|failed|errored|aborted). It is derived
	// server-side by joining the builds table on read and is never accepted
	// from the ingesting client; display surfaces render this as the run
	// truth so a green step status can never mask a failed build (U3).
	BuildStatus string `json:"build_status,omitempty"`
	// Outcome is the server-derived display truth for the run: BuildStatus
	// and Status fused by DeriveOutcome so every surface (web, fly) renders
	// the same verdict instead of re-deriving it. Like BuildStatus it is
	// computed on read and never accepted from the ingesting client. Empty
	// when the fusion is underivable (unknown status vocabulary) — and on
	// pre-outcome servers, so consumers fall back to DeriveOutcome locally.
	Outcome         string          `json:"outcome,omitempty"`
	Summary         string          `json:"summary"`
	Model           string          `json:"model"`
	Usage           Usage           `json:"usage"`
	Turns           int             `json:"turns"`
	WallTimeSeconds int             `json:"wall_time_seconds"`
	CostUSD         float64         `json:"cost_usd"`
	Results         json.RawMessage `json:"results,omitempty"`
	EventsArtifact  string          `json:"events_artifact,omitempty"`
	EventCounts     map[string]int  `json:"event_counts,omitempty"`
	CreatedAt       int64           `json:"created_at,omitempty"` // epoch seconds; set by the DB on read
	// IngestionSeq counts how many times this (BuildID, PlanID) has been
	// ingested. The row is upserted on that key, so a step that executes more
	// than once overwrites its own record; this is the only thing that survives
	// to say it happened. Server-assigned on write and never accepted from the
	// ingesting client.
	IngestionSeq int `json:"ingestion_seq,omitempty"`
}

// Run outcome tokens — the vocabulary of RunMetrics.Outcome. A superset of
// the step statuses: terminal build states (errored/aborted) and the fused
// display states (running/no_output) appear here but never in Status.
const (
	RunOutcomeOK         = "ok"
	RunOutcomeNoOutput   = "no_output"
	RunOutcomeRunning    = "running"
	RunOutcomeFailed     = "failed"
	RunOutcomeErrored    = "errored"
	RunOutcomeAborted    = "aborted"
	RunOutcomeUnrecorded = "unrecorded" // delivered, but no flight recording (L-1)
)

// HasResult reports whether the run delivered anything: a structured results
// payload or a non-empty summary. (Web surfaces without the results payload
// proxy this with summary-non-empty; the server has both, so a run whose
// summary went missing but whose results landed still counts as delivered.)
func (rm RunMetrics) HasResult() bool {
	return len(rm.Results) > 0 || rm.Summary != ""
}

// DeriveOutcome fuses BuildStatus (atc build-status vocabulary, joined on
// read) with the step's own Status into the single display truth (U3). It is
// THE definition of the rule — web/elm/src/AgentBadge.elm runOutcome mirrors
// it only as a fallback for servers that predate the Outcome field. The
// precedence is "worst truth wins":
//
//  1. A terminally-bad BUILD is final — failed/errored/aborted render as such
//     whatever the step's own row says.
//  2. Otherwise a step-reported failure ("error"/"failed") is never masked —
//     not by a succeeded build (attempts/try can fail an agent step inside a
//     green build) and not by a still-open one.
//  3. An "incomplete" step (the ingestion read no flight output) renders the
//     amber "unrecorded" — the step almost certainly delivered (the build did
//     not fail), we simply have no recording of it — except on a still-open
//     build, which reads "running". Never red on a non-failed build.
//  4. Only then does the build speak: succeeded is "ok" only with a result
//     in hand (else "no_output" — never a green verdict on a run that did
//     not deliver), started/pending render "running", and an absent or
//     unknown build status falls back to the step's own word ("" when even
//     that is unrecognized).
func (rm RunMetrics) DeriveOutcome() string {
	switch rm.BuildStatus {
	case "failed":
		return RunOutcomeFailed
	case "errored":
		return RunOutcomeErrored
	case "aborted":
		return RunOutcomeAborted
	}
	switch rm.Status {
	case RunStatusError:
		return RunOutcomeErrored
	case RunStatusFailed:
		return RunOutcomeFailed
	}
	// incomplete (L-1): the ingestion read no flight output. A terminally-bad
	// build already returned above; a still-open build is "running"; everything
	// else is the amber "unrecorded" — the step almost certainly delivered (the
	// build did not fail), we simply have no recording of it. Never red.
	if rm.Status == RunStatusIncomplete {
		switch rm.BuildStatus {
		case "started", "pending":
			return RunOutcomeRunning
		default:
			return RunOutcomeUnrecorded
		}
	}
	switch rm.BuildStatus {
	case "succeeded":
		if rm.HasResult() {
			return RunOutcomeOK
		}
		return RunOutcomeNoOutput
	case "started", "pending":
		return RunOutcomeRunning
	}
	if rm.Status == RunStatusOK {
		return RunOutcomeOK
	}
	return ""
}
