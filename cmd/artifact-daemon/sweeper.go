package main

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"code.cloudfoundry.org/lager/v3"

	"github.com/concourse/concourse/cmd/artifact-daemon/outputplane"
)

// Sweeper periodically removes expired artifacts from the hostPath storage.
// It sweeps two areas:
//   - /steps/<handle>/ directories: removed if the dir mtime exceeds the TTL.
//     Reads (resolve, GET) refresh the mtime, so only artifacts nothing has
//     touched for a full TTL are removed. A step whose pod is still running on
//     this node touches nothing while it runs, so the sweep asks the node and
//     refreshes the mtime itself: the TTL counts from the last pass that saw
//     the pod alive.
//   - Legacy flat files under /artifacts/: removed if mtime > TTL
//
// It does NOT sweep /caches/ — those are cleaned only by DB-driven GC.
type Sweeper struct {
	logger      lager.Logger
	storagePath string
	ttl         time.Duration
	interval    time.Duration
	registry    *Registry

	// guard serializes step-dir removal against in-flight reads. Nil-safe
	// (tests without a server); main wires the server's guard. Set before
	// Run starts.
	guard *ReadGuard

	// sourceLedger is the output plane's read-only source ledger.
	//
	// The sweeper is the destructive path a held source is most likely to
	// meet. Nothing refreshes a held source's mtime -- the producer wrote it
	// and exited, and the capture is waiting for a seal -- so a source waiting
	// to be sealed ages exactly like an abandoned one, and this loop is the
	// thing that decides. Tests may omit it; main always wires the server's
	// classifier, even when the output plane's routes are disabled.
	sourceLedger *outputplane.Classifier

	// stepPods is this node's step pod view. Nil (a daemon with no Kubernetes
	// client) protects no running step; main wires it whenever it can.
	stepPods StepPods
}

// SetStepPods wires the node's step pod view. Must be called before Run starts.
func (s *Sweeper) SetStepPods(pods StepPods) {
	s.stepPods = pods
}

// SetSourceLedger wires the output plane's read-only classifier. Must be
// called before Run starts.
func (s *Sweeper) SetSourceLedger(classifier *outputplane.Classifier) {
	s.sourceLedger = classifier
}

// SetGuard wires the read/sweep coordination guard. Must be called before
// Run starts.
func (s *Sweeper) SetGuard(g *ReadGuard) {
	s.guard = g
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
		// One list per pass. A pod view that cannot be read spares every step
		// directory this pass -- a running step's directory is not worth a
		// disk's worth of expired ones -- and restarts no clock, so the next
		// pass that can read it decides on the original mtimes.
		live, liveErr := s.liveStepPods()
		if liveErr != nil {
			logger.Error("step-pods-unavailable", liveErr)
		}
		now := time.Now()
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			handleDir := filepath.Join(stepsDir, entry.Name())
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if !info.ModTime().Before(cutoff) {
				continue
			}
			if liveErr != nil {
				continue
			}
			if _, running := live[entry.Name()]; running {
				if err := os.Chtimes(handleDir, now, now); err != nil {
					logger.Error("failed-to-refresh-running-step-dir", err, lager.Data{"path": handleDir})
				}
				logger.Debug("spared-running-step-dir", lager.Data{"path": handleDir})
				continue
			}
			if s.removeStepDir(logger, handleDir, entry.Name(), cutoff) {
				removed++
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

// liveStepPods asks the node which step pods are still running. Without a
// view there are none to spare.
func (s *Sweeper) liveStepPods() (map[string]struct{}, error) {
	if s.stepPods == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.stepPods.Live(ctx)
}

// removeStepDir deletes an expired step directory, coordinating with
// in-flight reads: it waits out active reads via the guard, then re-checks
// the mtime — a read that just completed touched the dir, in which case the
// dir is spared. Deleting without this coordination lets cp -R copy a
// half-removed tree and report success (partial artifact served as complete).
func (s *Sweeper) removeStepDir(logger lager.Logger, handleDir, handle string, cutoff time.Time) bool {
	release := s.guard.BeginSweep(handle)
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

	// The output plane's hold, under the same exclusive lock the removal takes.
	//
	// Asked HERE rather than in sweep() above: between the expiry check and
	// this point the guard has drained in-flight reads, and a hold established
	// in that window is a hold this sweep must see. The classifier reads the
	// ledger on every call for the same reason.
	//
	// A step directory is the PARENT of an incarnation, so this asks the
	// ancestor question -- the one the first version of the classifier could
	// not answer, and the reason this loop deleted held sources on a timer.
	if s.sourceLedger != nil {
		class := s.sourceLedger.Classify(handle)
		if !class.Destructive() {
			logger.Info("spared-capture-held-step-dir", lager.Data{
				"path": handleDir, "class": string(class),
			})

			return false
		}
	}

	if err := os.RemoveAll(handleDir); err != nil {
		logger.Error("failed-to-remove-step-dir", err, lager.Data{"path": handleDir})
		return false
	}
	if s.registry != nil {
		s.registry.RemoveByPath(handleDir)
	}
	return true
}

// SweepOnce performs a single sweep pass (for testing).
func (s *Sweeper) SweepOnce() {
	s.sweep()
}
