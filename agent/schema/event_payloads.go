package schema

// Event types per shared-contracts §5. Producers may add data keys but
// never repurpose them; consumers must ignore unknown keys and types.
const (
	EventStepStart  EventType = "step.start"
	EventStepEnd    EventType = "step.end"
	EventCostRecord EventType = "cost.record"
	EventMCPReady   EventType = "mcp.ready"
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

// MCPReadyData records only the negotiated managed MCP identity and tool names.
type MCPReadyData struct {
	Server          string   `json:"server"`
	ProtocolVersion string   `json:"protocol_version"`
	Tools           []string `json:"tools"`
}

type StepEndData struct {
	StepName        string  `json:"step_name"`
	Status          string  `json:"status"` // ok | failed | error
	Summary         string  `json:"summary"`
	WallTimeSeconds int     `json:"wall_time_seconds"`
	CostUSD         float64 `json:"cost_usd"`
	Turns           int     `json:"turns"`
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
