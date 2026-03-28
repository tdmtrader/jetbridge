package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"code.cloudfoundry.org/lager/v3"
)

// Registry is a thread-safe in-memory map from artifact key to the local
// disk path where the artifact is stored. It serves as the daemon's source
// of truth for what artifacts exist on this node.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]string // key → absolute disk path
	logger  lager.Logger
}

// NewRegistry creates an empty Registry.
func NewRegistry(logger lager.Logger) *Registry {
	return &Registry{
		entries: make(map[string]string),
		logger:  logger,
	}
}

// Register records that artifact `key` is stored at `localPath`.
func (r *Registry) Register(key, localPath string) {
	r.mu.Lock()
	r.entries[key] = localPath
	r.mu.Unlock()
	r.logger.Debug("registered", lager.Data{"key": key, "path": localPath})
}

// Lookup returns the local disk path for a key, or ("", false) if not found.
func (r *Registry) Lookup(key string) (string, bool) {
	r.mu.RLock()
	path, ok := r.entries[key]
	r.mu.RUnlock()
	return path, ok
}

// Remove deletes a key from the registry.
func (r *Registry) Remove(key string) {
	r.mu.Lock()
	delete(r.entries, key)
	r.mu.Unlock()
}

// Len returns the number of registered artifacts.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}

// Keys returns all registered keys (for diagnostics).
func (r *Registry) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	keys := make([]string, 0, len(r.entries))
	for k := range r.entries {
		keys = append(keys, k)
	}
	return keys
}

// ScanHostPath walks the storage directory and registers all step output
// directories as artifacts. This populates the registry at startup so that
// artifacts from previous builds (that haven't been swept yet) are servable.
//
// Directory structure:
//
//	<storagePath>/artifacts/steps/<handle>/<output>/
//
// Each <handle> directory is registered with key = handle and path = the
// full disk path to the handle directory.
func (r *Registry) ScanHostPath(storagePath string) error {
	stepsDir := filepath.Join(storagePath, "artifacts", "steps")
	entries, err := os.ReadDir(stepsDir)
	if err != nil {
		if os.IsNotExist(err) {
			r.logger.Info("scan-no-steps-dir", lager.Data{"path": stepsDir})
			return nil
		}
		return fmt.Errorf("reading steps directory: %w", err)
	}

	registered := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		handle := entry.Name()
		handlePath := filepath.Join(stepsDir, handle)

		// Register each output subdirectory within the handle.
		outputs, err := os.ReadDir(handlePath)
		if err != nil {
			r.logger.Error("scan-read-handle-dir", err, lager.Data{"handle": handle})
			continue
		}
		for _, output := range outputs {
			if !output.IsDir() {
				continue
			}
			// The key is the volume handle. The path is the absolute disk
			// path to the specific output directory.
			// Multiple outputs per handle are registered separately — the
			// ATC records each output volume with its own key.
			outputPath := filepath.Join(handlePath, output.Name())
			r.Register(handle+"/"+output.Name(), outputPath)
			registered++
		}
	}

	r.logger.Info("scan-complete", lager.Data{
		"steps_dir":  stepsDir,
		"registered": registered,
	})
	return nil
}
