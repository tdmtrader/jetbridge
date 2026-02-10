package schema

import "encoding/json"

// EventType identifies the kind of event in an NDJSON event log.
type EventType string

const (
	EventAgentStart     EventType = "agent.start"
	EventAgentEnd       EventType = "agent.end"
	EventSkillStart     EventType = "skill.start"
	EventSkillEnd       EventType = "skill.end"
	EventToolCall       EventType = "tool.call"
	EventToolResult     EventType = "tool.result"
	EventArtifactWriten EventType = "artifact.written"
	EventDecision       EventType = "decision"
	EventError          EventType = "error"
)

// Event represents a single line in the events.ndjson log.
type Event struct {
	Timestamp string                 `json:"ts"`
	Type      EventType              `json:"event"`
	Data      map[string]interface{} `json:"data"`
}

// Validate checks that all required fields are present and valid.
// Implementation is a separate task (Task 6).
func (e *Event) Validate() error {
	return nil
}

// MarshalJSON implements json.Marshaler. It ensures Data is serialized as
// an empty object rather than null when the map is nil.
func (e Event) MarshalJSON() ([]byte, error) {
	type Alias Event
	a := Alias(e)
	if a.Data == nil {
		a.Data = map[string]interface{}{}
	}
	return json.Marshal(a)
}
