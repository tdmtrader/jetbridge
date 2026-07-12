package schema

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// EventReader reads events line-by-line from an NDJSON stream.
// Each call to Read returns the next valid event or an error.
type EventReader struct {
	scanner *bufio.Scanner
	line    int
}

// maxEventLine caps a single NDJSON line at 5 MiB. bufio.Scanner's default
// 64 KiB token limit is too small for a large event (e.g. a tool.call carrying
// captured output): the line surfaces bufio.ErrTooLong, and because agent-step
// ingestion breaks its read loop on any reader error, a single oversized line
// mid-stream would silently discard every later cost.record and step.end event
// — leaving the step as status=error even when a valid step.end followed
// (review finding, 2026-07-12; contract 5 invites other producers to append
// events).
const maxEventLine = 5 << 20

// NewEventReader creates an EventReader that reads from r.
func NewEventReader(r io.Reader) *EventReader {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	return &EventReader{
		scanner: scanner,
	}
}

// Read returns the next event from the NDJSON stream. It skips empty lines.
// Returns io.EOF when no more events are available. Parse and validation
// errors include the line number.
func (er *EventReader) Read() (*Event, error) {
	for er.scanner.Scan() {
		er.line++
		line := strings.TrimSpace(er.scanner.Text())
		if line == "" {
			continue
		}

		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("line %d: invalid JSON: %w", er.line, err)
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
