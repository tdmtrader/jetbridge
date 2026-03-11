package schema

import (
	"encoding/json"
	"io"
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
// Returns an error if validation fails or writing fails. Invalid events
// are never written.
func (ew *EventWriter) Write(e Event) error {
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
