package schema

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType identifies the kind of event in an NDJSON event log.
type EventType string

const (
	EventAgentStart           EventType = "agent.start"
	EventAgentEnd             EventType = "agent.end"
	EventSkillStart           EventType = "skill.start"
	EventSkillEnd             EventType = "skill.end"
	EventToolCall             EventType = "tool.call"
	EventToolResult           EventType = "tool.result"
	EventArtifactWritten      EventType = "artifact.written"
	EventDecision             EventType = "decision"
	EventError                EventType = "error"
	EventPlanInputParsed      EventType = "plan.input_parsed"
	EventPlanSpecGenerated    EventType = "plan.spec_generated"
	EventPlanPlanGenerated    EventType = "plan.plan_generated"
	EventPlanConfidenceScored EventType = "plan.confidence_scored"
)

// Event represents a single line in the events.ndjson log.
type Event struct {
	Timestamp string          `json:"ts"`
	Type      EventType       `json:"event"`
	Data      json.RawMessage `json:"data"`
}

// Validate checks that all required fields are present and valid.
func (e *Event) Validate() error {
	if e.Timestamp == "" {
		return fmt.Errorf("ts is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.Timestamp); err != nil {
		return fmt.Errorf("ts must be a valid RFC3339 timestamp: %w", err)
	}
	if e.Type == "" {
		return fmt.Errorf("event type is required")
	}
	if len(e.Data) == 0 {
		return fmt.Errorf("data is required")
	}
	return nil
}

// MarshalJSON implements json.Marshaler. It ensures Data is serialized as
// an empty object rather than null when it is unset.
func (e Event) MarshalJSON() ([]byte, error) {
	type Alias Event
	a := Alias(e)
	if len(a.Data) == 0 {
		a.Data = json.RawMessage(`{}`)
	}
	return json.Marshal(a)
}
