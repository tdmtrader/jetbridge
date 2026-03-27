package main

import (
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// Sweeper periodically removes expired artifacts from the hostPath storage.
// It sweeps two areas:
//   - /steps/<handle>/ directories: removed if mtime > TTL (and no active pod)
//   - Legacy flat files under /artifacts/: removed if mtime > TTL
//
// It does NOT sweep /caches/ — those are cleaned only by DB-driven GC.
type Sweeper struct {
	logger      lager.Logger
	storagePath string
	ttl         time.Duration
	interval    time.Duration
}

// NewSweeper creates a new Sweeper.
func NewSweeper(logger lager.Logger, storagePath string, ttl, interval time.Duration) *Sweeper {
	return &Sweeper{
		logger:      logger,
		storagePath: storagePath,
		ttl:         ttl,
		interval:    interval,
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
	stepsDir := filepath.Join(s.storagePath, "artifacts", "steps")
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
				if err := os.RemoveAll(handleDir); err != nil {
					logger.Error("failed-to-remove-step-dir", err, lager.Data{"path": handleDir})
				} else {
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

// SweepOnce performs a single sweep pass (for testing).
func (s *Sweeper) SweepOnce() {
	s.sweep()
}
