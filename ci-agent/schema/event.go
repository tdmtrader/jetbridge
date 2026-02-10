package schema

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// EventType constants for agent events.
type EventType string

const (
	EventAgentStart         EventType = "agent.start"
	EventAgentEnd           EventType = "agent.end"
	EventSkillStart         EventType = "skill.start"
	EventSkillEnd           EventType = "skill.end"
	EventArtifactWritten    EventType = "artifact.written"
	EventDecision           EventType = "decision"
	EventError              EventType = "error"
	EventPlanInputParsed    EventType = "plan.input_parsed"
	EventPlanSpecGenerated  EventType = "plan.spec_generated"
	EventPlanPlanGenerated  EventType = "plan.plan_generated"
	EventPlanConfidenceScored EventType = "plan.confidence_scored"
)

// Event represents a single line in events.ndjson.
type Event struct {
	Timestamp string          `json:"ts"`
	EventType EventType       `json:"event"`
	Data      json.RawMessage `json:"data"`
}

// Validate checks that all required Event fields are present.
func (e *Event) Validate() error {
	if e.Timestamp == "" {
		return fmt.Errorf("ts is required")
	}
	if _, err := time.Parse(time.RFC3339, e.Timestamp); err != nil {
		return fmt.Errorf("invalid timestamp %q: %w", e.Timestamp, err)
	}
	if e.EventType == "" {
		return fmt.Errorf("event is required")
	}
	if len(e.Data) == 0 {
		return fmt.Errorf("data is required")
	}
	return nil
}

// EventWriter writes events as NDJSON to an io.Writer.
type EventWriter struct {
	w io.Writer
}

// NewEventWriter creates a new EventWriter.
func NewEventWriter(w io.Writer) *EventWriter {
	return &EventWriter{w: w}
}

// Write writes a single event as a JSON line. Sets timestamp if missing.
func (ew *EventWriter) Write(event Event) error {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(ew.w, "%s\n", data)
	return err
}

// EventReader reads events from NDJSON.
type EventReader struct {
	scanner *bufio.Scanner
	line    int
}

// NewEventReader creates a new EventReader.
func NewEventReader(r io.Reader) *EventReader {
	return &EventReader{scanner: bufio.NewScanner(r)}
}

// Read reads the next event. Returns io.EOF when done.
func (er *EventReader) Read() (*Event, error) {
	for er.scanner.Scan() {
		er.line++
		text := er.scanner.Text()
		if text == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(text), &event); err != nil {
			return nil, fmt.Errorf("line %d: %w", er.line, err)
		}
		if err := event.Validate(); err != nil {
			return nil, fmt.Errorf("line %d: %w", er.line, err)
		}
		return &event, nil
	}
	if err := er.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}
