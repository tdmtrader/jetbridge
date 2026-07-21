package harvest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFlightRecorderWriteJSONReturnsWriteError(t *testing.T) {
	dir := t.TempDir()
	notDirectory := filepath.Join(dir, "file")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder := &flightRecorder{dir: notDirectory}
	if err := recorder.writeJSON("diff.json", map[string]string{"ok": "yes"}); err == nil {
		t.Fatal("expected write error")
	}
}
