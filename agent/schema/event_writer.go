package schema

import (
	"encoding/json"
	"io"
	"time"
)

// EventWriter appends events to an io.Writer as newline-delimited JSON.
// Each Write call produces exactly one JSON line followed by a newline.
type EventWriter struct {
	w io.Writer
}

// NewEventWriter creates an EventWriter that writes to w.
func NewEventWriter(w io.Writer) *EventWriter {
	return &EventWriter{w: w}
}

// Write validates the event and appends it as a single JSON line.
// A missing timestamp is set to the current UTC time before validation.
// Returns an error if validation fails or writing fails. Invalid events
// are never written.
func (ew *EventWriter) Write(e Event) error {
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	if err := e.Validate(); err != nil {
		return err
	}

	data, err := json.Marshal(e)
	if err != nil {
		return err
	}

	data = append(data, '\n')
	_, err = ew.w.Write(data)
	return err
}
