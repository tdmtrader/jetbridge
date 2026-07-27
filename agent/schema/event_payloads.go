package schema

// New event types per shared-contracts §5. Producers may add data keys but
// never repurpose them; consumers must ignore unknown keys and types.
const (
	EventStepStart      EventType = "step.start"
	EventStepEnd        EventType = "step.end"
	EventGateStart      EventType = "gate.start"
	EventGateResult     EventType = "gate.result"
	EventSubagentCall   EventType = "subagent.call"
	EventSubagentResult EventType = "subagent.result"
	EventCostRecord     EventType = "cost.record"
	EventBudgetWarn     EventType = "budget.warn"
	EventBudgetStop     EventType = "budget.stop"
	EventJudgeScore     EventType = "judge.score"
	EventPushDone       EventType = "push.done"
)

// StepStartData is the pod's view of the step: (build_id, plan_id) is the §5
// correlation key back to the agent_run_metrics row, which carries the durable
// workflow identity server-side. The pod is never told which workflow run it
// belongs to.
type StepStartData struct {
	StepName       string  `json:"step_name"`
	BuildID        int     `json:"build_id"`
	PlanID         string  `json:"plan_id"`
	BudgetSliceUSD float64 `json:"budget_slice_usd,omitempty"`
}

type StepEndData struct {
	StepName        string  `json:"step_name"`
	Status          string  `json:"status"` // ok | failed | error
	Summary         string  `json:"summary"`
	WallTimeSeconds int     `json:"wall_time_seconds"`
	CostUSD         float64 `json:"cost_usd"`
	Turns           int     `json:"turns"`
}

type GateStartData struct {
	Gate      string `json:"gate"` // build | test | lint
	Component string `json:"component"`
	Scope     string `json:"scope"` // affected | full
}

type GateResultData struct {
	Gate            string  `json:"gate"`
	Component       string  `json:"component"`
	Scope           string  `json:"scope"`
	Status          string  `json:"status"`
	DurationSeconds float64 `json:"duration_seconds"`
	Summary         string  `json:"summary"`
	LogArtifact     string  `json:"log_artifact,omitempty"`
	// Attempt/Flaky surface the §6.3 flake stance (2026-07-17 harvest
	// addendum): a pass on attempt N>1 is ok + flaky:true — flakiness is
	// surfaced, never hidden. Omitted on first-attempt results.
	Attempt int  `json:"attempt,omitempty"`
	Flaky   bool `json:"flaky,omitempty"`
}

type SubagentCallData struct {
	CallID      string `json:"call_id"`
	Tool        string `json:"tool"` // request_review | ask_agent
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	PromptChars int    `json:"prompt_chars"`
}

type SubagentResultData struct {
	CallID       string  `json:"call_id"`
	Status       string  `json:"status"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	Turns        int     `json:"turns"`
	CostUSD      float64 `json:"cost_usd"`
	DurationMS   int     `json:"duration_ms"`
	FindingCount *int    `json:"finding_count,omitempty"`
}

// CostRecordData mirrors budget.LedgerEntry (shared-contracts §2.7 / §1.4).
type CostRecordData struct {
	Source              string  `json:"source"`
	Provider            string  `json:"provider"`
	Model               string  `json:"model"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	CacheReadTokens     int64   `json:"cache_read_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	Turns               int     `json:"turns"`
	CostUSD             float64 `json:"cost_usd"`
}

type BudgetData struct {
	Scope        string  `json:"scope"` // step | ticket | daily
	LimitUSD     float64 `json:"limit_usd"`
	SpentUSD     float64 `json:"spent_usd"`
	RemainingUSD float64 `json:"remaining_usd"`
}

type JudgeScoreDimension struct {
	Name      string  `json:"name"`
	Score     float64 `json:"score"`
	Max       float64 `json:"max"`
	Rationale string  `json:"rationale"`
}

type JudgeScoreData struct {
	RubricHash string                `json:"rubric_hash"`
	Dimensions []JudgeScoreDimension `json:"dimensions"`
	Total      float64               `json:"total"`
	MaxTotal   float64               `json:"max_total"`
	Model      string                `json:"model"`
	CostUSD    float64               `json:"cost_usd"`
}

type PushDoneData struct {
	Branch           string `json:"branch"`
	Sha              string `json:"sha"`
	ManifestArtifact string `json:"manifest_artifact"`
}
