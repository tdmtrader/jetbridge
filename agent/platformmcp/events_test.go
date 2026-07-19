package platformmcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/concourse/concourse/agent/platformmcp"
)

func TestEventLogWritesNDJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.ndjson")
	log, err := platformmcp.NewEventLog(path)
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("human.ask", map[string]any{"question_id": 7, "kind": "question"})
	log.Emit("human.answer", map[string]any{"question_id": 7, "answer": "yes"})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 NDJSON lines, got %d: %q", len(lines), raw)
	}
	var first struct {
		TS    string         `json:"ts"`
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Event != "human.ask" || first.TS == "" || first.Data["question_id"] != float64(7) {
		t.Fatalf("unexpected event line: %+v", first)
	}
}

func TestEventLogStdoutFallbackNeverPanics(t *testing.T) {
	log, err := platformmcp.NewEventLog("")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("human.ask", map[string]any{"question_id": 1})
}
