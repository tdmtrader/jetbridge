package main

import (
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"
)

// Sweeper periodically removes artifacts older than the configured TTL.
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

// Sweep performs a single pass, removing files older than TTL.
func (s *Sweeper) sweep() {
	logger := s.logger.Session("sweep")
	cutoff := time.Now().Add(-s.ttl)
	removed := 0

	artifactsDir := filepath.Join(s.storagePath, "artifacts")
	err := filepath.Walk(artifactsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible files
		}
		if info.IsDir() {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			if removeErr := os.Remove(path); removeErr != nil {
				logger.Error("failed-to-remove", removeErr, lager.Data{"path": path})
			} else {
				removed++
			}
		}
		return nil
	})

	if err != nil {
		logger.Error("walk-failed", err)
	}

	if removed > 0 {
		logger.Info("completed", lager.Data{"removed": removed})
	}
}

// SweepOnce performs a single sweep pass (for testing).
func (s *Sweeper) SweepOnce() {
	s.sweep()
}
