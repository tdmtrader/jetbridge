package platformmcp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// TuneShortPark overrides the §A threshold and §B1 sentinel path (tests only).
func (s *Server) TuneShortPark(threshold time.Duration, parkPath string) {
	s.cfg.ShortParkMax = threshold
	s.cfg.ParkPath = parkPath
}

// armParkExit starts the PARK-V2 §A threshold timer for a park on q and
// returns a stop func the caller MUST defer: an answered park must never fire
// a stale sentinel. The timer measures from the row's asked_at — a joiner
// (a dedup'd re-ask) therefore inherits the ORIGINAL park clock, never a
// fresh one.
func (s *Server) armParkExit(q *Question) func() {
	if s.cfg.ShortParkMax <= 0 {
		return func() {} // 0 = never exit (pure PARK-V1; the rollback hatch)
	}
	remaining := time.Until(time.Unix(q.AskedAt, 0).Add(s.cfg.ShortParkMax))
	if remaining < 0 {
		remaining = 0
	}
	timer := time.AfterFunc(remaining, func() { s.crossParkThreshold(q) })
	return func() { timer.Stop() }
}

// crossParkThreshold writes the §B1 sentinel. The question row is NOT touched
// — it stays open as the durable representation of the wait (§B3); the
// agent-runner's 5s stat loop sees the file, SIGTERMs claude, and this call's
// connection drops (AwaitAnswer cancels on the request context). A pod with
// no PLATFORM_MCP_PARK_PATH — an agent-step-exec bug in an agent-step pod
// (the exec sets it via SidecarEnv, F15 — plan 07 Task 26); normal in
// checkpoint pods, which exit via the 202 response instead — degrades LOUDLY
// to the PARK-V1 SSE park, bounded platform-side by --agent-park-timeout.
func (s *Server) crossParkThreshold(q *Question) {
	if s.cfg.ParkPath == "" {
		log.Printf("park-exit: question %d crossed the %s short-park threshold but PLATFORM_MCP_PARK_PATH is unset — staying on the SSE park (PARK-V1 degradation)", q.ID, s.cfg.ShortParkMax)
		return
	}
	payload := map[string]any{
		"question_id":       q.ID,
		"kind":              q.Kind,
		"step_name":         s.cfg.StepName,
		"asked_at":          time.Unix(q.AskedAt, 0).UTC().Format(time.RFC3339),
		"threshold_seconds": int(s.cfg.ShortParkMax / time.Second),
		"crossed_at":        time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeSentinelAtomic(s.cfg.ParkPath, payload); err != nil {
		log.Printf("park-exit: question %d: failed to write park sentinel %s: %s — staying on the SSE park", q.ID, s.cfg.ParkPath, err)
	}
}

// writeSentinelAtomic is §B1's write-temp-then-mv: the temp file lives in the
// SAME directory (same filesystem), so the rename is atomic and the runner's
// stat loop can never observe a partial file.
func writeSentinelAtomic(path string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	tmp := filepath.Join(filepath.Dir(path), filepath.Base(path)+".tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("renaming sentinel into place: %w", err)
	}
	return nil
}
