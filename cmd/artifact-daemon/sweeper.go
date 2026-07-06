package main

import (
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// Sweeper periodically removes expired artifacts from the hostPath storage.
// It sweeps two areas:
//   - /steps/<handle>/ directories: removed if the dir mtime exceeds the TTL.
//     Reads (resolve, GET) refresh the mtime, so only artifacts nothing has
//     touched for a full TTL are removed.
//   - Legacy flat files under /artifacts/: removed if mtime > TTL
//
// It does NOT sweep /caches/ — those are cleaned only by DB-driven GC.
type Sweeper struct {
	logger      lager.Logger
	storagePath string
	ttl         time.Duration
	interval    time.Duration
	registry    *Registry

	// onStepDirRemoved is called with the handle name after a step
	// directory is removed, so other components (the mirror status map)
	// can drop their bookkeeping for it. Set before Run starts.
	onStepDirRemoved func(handle string)

	// guard serializes step-dir removal against in-flight reads. Nil-safe
	// (tests without a server); main wires the server's guard. Set before
	// Run starts.
	guard *ReadGuard
}

// SetGuard wires the read/sweep coordination guard. Must be called before
// Run starts.
func (s *Sweeper) SetGuard(g *ReadGuard) {
	s.guard = g
}

// SetOnStepDirRemoved registers a callback invoked with the handle name each
// time a step directory is swept. Must be called before Run starts.
func (s *Sweeper) SetOnStepDirRemoved(fn func(handle string)) {
	s.onStepDirRemoved = fn
}

// NewSweeper creates a new Sweeper. The registry parameter is used to clean
// up alias entries when step directories are removed. Pass nil to skip alias
// cleanup (e.g. in tests that don't need it).
func NewSweeper(logger lager.Logger, storagePath string, ttl, interval time.Duration, registry *Registry) *Sweeper {
	return &Sweeper{
		logger:      logger,
		storagePath: storagePath,
		ttl:         ttl,
		interval:    interval,
		registry:    registry,
	}
}

// Run starts the sweep loop. It blocks until the done channel is closed.
func (s *Sweeper) Run(done <-chan struct{}) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

func (s *Sweeper) sweep() {
	logger := s.logger.Session("sweep")
	cutoff := time.Now().Add(-s.ttl)
	removed := 0

	// Sweep step directories: each child of /steps/ is a container handle dir.
	stepsDir := filepath.Join(s.storagePath, "steps")
	entries, err := os.ReadDir(stepsDir)
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			handleDir := filepath.Join(stepsDir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				if s.removeStepDir(logger, handleDir, entry.Name(), cutoff) {
					removed++
				}
			}
		}
	}

	// Sweep legacy flat files under /artifacts/ (backward compat).
	// Skip /steps/ and /caches/ subdirectories.
	artifactsDir := filepath.Join(s.storagePath, "artifacts")
	legacyEntries, err := os.ReadDir(artifactsDir)
	if err == nil {
		for _, entry := range legacyEntries {
			if entry.IsDir() {
				continue // skip steps/, caches/, and other subdirs
			}
			filePath := filepath.Join(artifactsDir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				if err := os.Remove(filePath); err != nil {
					logger.Error("failed-to-remove", err, lager.Data{"path": filePath})
				} else {
					removed++
				}
			}
		}
	}

	if removed > 0 {
		logger.Info("completed", lager.Data{"removed": removed})
	}
}

// removeStepDir deletes an expired step directory, coordinating with
// in-flight reads: it waits out active reads via the guard, then re-checks
// the mtime — a read that just completed touched the dir, in which case the
// dir is spared. Deleting without this coordination lets cp -R copy a
// half-removed tree and report success (partial artifact served as complete).
func (s *Sweeper) removeStepDir(logger lager.Logger, handleDir, handle string, cutoff time.Time) bool {
	release := s.guard.BeginSweep()
	defer release()

	// Re-stat under the exclusive lock: an in-flight read refreshed the
	// mtime, so the earlier expiry check may be stale.
	info, err := os.Stat(handleDir)
	if err != nil {
		return false
	}
	if !info.ModTime().Before(cutoff) {
		logger.Debug("spared-recently-read-step-dir", lager.Data{"path": handleDir})
		return false
	}

	if err := os.RemoveAll(handleDir); err != nil {
		logger.Error("failed-to-remove-step-dir", err, lager.Data{"path": handleDir})
		return false
	}
	if s.registry != nil {
		s.registry.RemoveByPath(handleDir)
	}
	if s.onStepDirRemoved != nil {
		s.onStepDirRemoved(handle)
	}
	return true
}

// SweepOnce performs a single sweep pass (for testing).
func (s *Sweeper) SweepOnce() {
	s.sweep()
}
