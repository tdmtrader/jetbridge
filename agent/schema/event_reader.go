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

// NewEventReader creates an EventReader that reads from r.
func NewEventReader(r io.Reader) *EventReader {
	return &EventReader{
		scanner: bufio.NewScanner(r),
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
