package schema

import "encoding/json"

// Usage captures token consumption from an LLM call. JSON field names match
// the claude CLI envelope (and ci-agent/llm.Usage).
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// RunMetrics is one agent step's flight-recorder rollup — both the ingest
// payload for SubmitAgentRunMetrics and the row shape of agent_run_metrics
// (shared-contracts §2.4 / §1.8).
type RunMetrics struct {
	TicketID        *int   `json:"ticket_id,omitempty"`
	PipelineRunID   *int   `json:"pipeline_run_id,omitempty"`
	BuildID         int    `json:"build_id"`
	PlanID          string `json:"plan_id"`
	StepName        string `json:"step_name"`
	WorkflowName    string `json:"workflow_name,omitempty"`
	WorkflowVersion *int   `json:"workflow_version,omitempty"`
	WorkflowHash    string `json:"workflow_hash,omitempty"`
	Status          string `json:"status"` // ok | failed | error — the AGENT STEP exit status
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
}

// Run outcome tokens — the vocabulary of RunMetrics.Outcome. A superset of
// the step statuses: terminal build states (errored/aborted) and the fused
// display states (running/no_output) appear here but never in Status.
const (
	RunOutcomeOK       = "ok"
	RunOutcomeNoOutput = "no_output"
	RunOutcomeRunning  = "running"
	RunOutcomeParked   = "parked"
	RunOutcomeFailed   = "failed"
	RunOutcomeErrored  = "errored"
	RunOutcomeAborted  = "aborted"
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
//     even if the metric row still says "parked" (an abort-while-parked
//     leaves the parked row behind forever; it must not read as "waiting on
//     a human" for a dead run).
//  2. Otherwise parked beats everything: the build deliberately stays
//     "started" while a HITL checkpoint waits, and a merely-open (or even
//     succeeded) build must not hide that the operator is needed.
//  3. Otherwise a step-reported failure ("error"/"failed") is never masked —
//     not by a succeeded build (attempts/try can fail an agent step inside a
//     green build) and not by a still-open one.
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
	case RunStatusParked:
		return RunOutcomeParked
	case RunStatusError:
		return RunOutcomeErrored
	case RunStatusFailed:
		return RunOutcomeFailed
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
