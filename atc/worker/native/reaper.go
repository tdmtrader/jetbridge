package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/tracing"
)

// Reaper cleans up local scratch directories for containers that have been
// marked as "destroying" in the database. On its first run, it performs a
// startup sweep to kill orphaned processes from a previous ATC instance.
type Reaper struct {
	logger         lager.Logger
	cfg            Config
	containerRepo  db.ContainerRepository
	startupDone    bool
}

// NewReaper creates a Reaper for the native worker.
func NewReaper(logger lager.Logger, cfg Config, containerRepo db.ContainerRepository) *Reaper {
	return &Reaper{
		logger:        logger,
		cfg:           cfg,
		containerRepo: containerRepo,
	}
}

// Run implements component.Runnable. It performs cleanup on each invocation.
func (r *Reaper) Run(ctx context.Context) error {
	logger := r.logger.Session("reap")

	ctx, span := tracing.StartSpan(ctx, "native.reaper.run", tracing.Attrs{
		"worker-name": r.cfg.WorkerName,
	})
	var spanErr error
	defer func() { tracing.End(span, spanErr) }()

	// On first run, clean up orphaned processes from a previous ATC instance.
	if !r.startupDone {
		r.startupSweep(logger)
		r.startupDone = true
	}

	// Clean up containers in destroying state. FindDestroyingContainers
	// returns a list of container handle strings.
	handles, err := r.containerRepo.FindDestroyingContainers(r.cfg.WorkerName)
	if err != nil {
		logger.Error("failed-to-find-destroying-containers", err)
		spanErr = err
		return fmt.Errorf("find destroying containers: %w", err)
	}

	// RemoveDestroyingContainers' second parameter is handlesToKeep —
	// handles that should NOT be removed from the DB. We track handles
	// where local cleanup failed so they remain in "destroying" state
	// for the next reap cycle.
	var failedHandles []string
	for _, handle := range handles {
		containerDir := filepath.Join(r.cfg.WorkDir, "containers", handle)

		// Kill any process that might still be running.
		r.killFromPidFile(logger, containerDir, handle)

		// Remove the scratch directory.
		if err := os.RemoveAll(containerDir); err != nil {
			logger.Error("failed-to-remove-container-dir", err, lager.Data{"handle": handle})
			failedHandles = append(failedHandles, handle)
			continue
		}

		logger.Info("reaped-container", lager.Data{"handle": handle})
	}

	// Remove successfully cleaned containers from the DB. Pass failed
	// handles as the "keep" list so they stay in destroying state.
	if len(handles) > 0 {
		_, err := r.containerRepo.RemoveDestroyingContainers(r.cfg.WorkerName, failedHandles)
		if err != nil {
			logger.Error("failed-to-remove-destroying-containers", err)
		}
	}

	return nil
}

// startupSweep scans the container scratch directory for leftover directories
// from a previous ATC instance. For each, it kills any matching OS process
// and removes the directory.
func (r *Reaper) startupSweep(logger lager.Logger) {
	logger = logger.Session("startup-sweep")

	containersDir := filepath.Join(r.cfg.WorkDir, "containers")
	entries, err := os.ReadDir(containersDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		logger.Error("failed-to-read-containers-dir", err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		handle := entry.Name()
		containerDir := filepath.Join(containersDir, handle)

		// Kill any orphaned process.
		r.killFromPidFile(logger, containerDir, handle)

		// Remove the scratch directory. If a container is still active in
		// the DB, it will be recreated when the step runs.
		if err := os.RemoveAll(containerDir); err != nil {
			logger.Error("failed-to-remove-stale-container-dir", err, lager.Data{"handle": handle})
		} else {
			logger.Info("cleaned-up-stale-container", lager.Data{"handle": handle})
		}
	}
}

// killFromPidFile reads a PID file and sends SIGKILL to the process if it's
// still running.
func (r *Reaper) killFromPidFile(logger lager.Logger, containerDir, handle string) {
	pidFile := filepath.Join(containerDir, handle+".pid")
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return // No PID file means no process to kill.
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return
	}

	// Check if the process still exists.
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}

	// Signal 0 checks if process exists without actually sending a signal.
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return // Process doesn't exist.
	}

	logger.Info("killing-orphaned-process", lager.Data{"handle": handle, "pid": pid})
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
