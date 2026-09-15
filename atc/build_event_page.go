package atc

import "encoding/json"

type BuildEventPageRequest struct {
	Cursor   string
	MaxBytes int
}
type BuildEventRecord struct {
	ID      string          `json:"id"`
	Event   string          `json:"event"`
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data"`
}

// Finished and CaughtUp refer to one committed snapshot. Retain NextCursor to
// revalidate it: existing event writers may commit late even after completion.
type BuildEventPage struct {
	Events     []BuildEventRecord `json:"events"`
	NextCursor *string            `json:"next_cursor"`
	Finished   bool               `json:"finished"`
	CaughtUp   bool               `json:"caught_up"`
	Truncated  bool               `json:"truncated"`
}
