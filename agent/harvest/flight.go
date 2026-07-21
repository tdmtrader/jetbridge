package harvest

import (
	"encoding/json"
	"os"
	"path/filepath"

	schema "github.com/concourse/concourse/agent/schema"
)

// flightRecorder owns the §2.8.1 flight-dir outputs (events.ndjson,
// results.json, manifest.json, review.json). A nil recorder (no
// AGENT_FLIGHT_DIR — the pre-flight-recorder exec) is a no-op on every
// method: the runner must keep working under the deployed v0.5 exec.
// Recorder failures never break harvest control flow — evidence is
// best-effort, the exit code is the contract.
type flightRecorder struct {
	dir     string
	events  *schema.EventWriter
	eventsF *os.File
}

func newFlightRecorder(dir string) (*flightRecorder, error) {
	if dir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(filepath.Join(dir, "events.ndjson"))
	if err != nil {
		return nil, err
	}
	return &flightRecorder{dir: dir, events: schema.NewEventWriter(f), eventsF: f}, nil
}

// eventWriter exposes the writer for RunGates' live gate events; nil
// when there is no flight dir.
func (r *flightRecorder) eventWriter() *schema.EventWriter {
	if r == nil {
		return nil
	}
	return r.events
}

// emit writes one event; the timestamp is set by EventWriter.Write.
func (r *flightRecorder) emit(t schema.EventType, payload any) {
	if r == nil {
		return
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_ = r.events.Write(schema.Event{Type: t, Data: data})
}

// writeJSON writes one flight file. Callers decide whether a failure is
// diagnostic-only or prevents the operation from continuing.
func (r *flightRecorder) writeJSON(name string, v any) error {
	if r == nil {
		return nil
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(r.dir, name), append(data, '\n'), 0o644)
}

func (r *flightRecorder) close() {
	if r == nil {
		return
	}
	_ = r.eventsF.Close()
}
