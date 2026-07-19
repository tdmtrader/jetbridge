package platformmcp

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/schema"
)

// EventLog appends §5 flight-recorder events as NDJSON. Path "" = stdout
// (pod logs); a file path is used when the pod shares an events volume
// (PLATFORM_MCP_EVENTS_PATH, Task 1 addendum). Emission is best-effort:
// a broken event log must never fail a tool call.
type EventLog struct {
	mu sync.Mutex
	w  *schema.EventWriter
}

func NewEventLog(path string) (*EventLog, error) {
	if path == "" {
		return &EventLog{w: schema.NewEventWriter(os.Stdout)}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &EventLog{w: schema.NewEventWriter(f)}, nil
}

// Emit writes one §5 event line. The data map is marshalled into the
// schema.Event's raw data field (the landed schema carries json.RawMessage,
// not a map); a nil map becomes {} so the schema validator never rejects
// the line. Errors are swallowed by design (best-effort recorder).
func (l *EventLog) Emit(eventType schema.EventType, data map[string]interface{}) {
	raw := json.RawMessage(`{}`)
	if data != nil {
		marshalled, err := json.Marshal(data)
		if err != nil {
			return
		}
		raw = marshalled
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.w.Write(schema.Event{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      eventType,
		Data:      raw,
	})
}
