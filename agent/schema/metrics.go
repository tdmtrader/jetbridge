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
	TicketID        *int            `json:"ticket_id,omitempty"`
	PipelineRunID   *int            `json:"pipeline_run_id,omitempty"`
	BuildID         int             `json:"build_id"`
	PlanID          string          `json:"plan_id"`
	StepName        string          `json:"step_name"`
	WorkflowName    string          `json:"workflow_name,omitempty"`
	WorkflowVersion *int            `json:"workflow_version,omitempty"`
	WorkflowHash    string          `json:"workflow_hash,omitempty"`
	Status          string          `json:"status"` // ok | failed | error
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
