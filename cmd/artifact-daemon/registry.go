package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"code.cloudfoundry.org/lager/v3"
)

// Registry is a thread-safe in-memory map from artifact key to the local
// disk path where the artifact is stored. It serves as the daemon's source
// of truth for what artifacts exist on this node.
//
// Entries come from two sources:
//   - ScanHostPath: discovers "containerHandle/output" keys from the directory tree
//   - RegisterAlias: records "volumeHandle" → disk path mappings from the ATC
//
// Only alias entries are persisted to disk (via AliasStore) because scan
// entries are always recoverable from the directory structure.
type Registry struct {
	mu         sync.RWMutex
	entries    map[string]string // key → absolute disk path (all entries)
	aliases    map[string]string // key → absolute disk path (alias entries only, persisted)
	aliasStore *AliasStore       // optional persistence; nil disables persistence
	logger     lager.Logger
}

// NewRegistry creates an empty Registry.
func NewRegistry(logger lager.Logger) *Registry {
	return &Registry{
		entries: make(map[string]string),
		aliases: make(map[string]string),
		logger:  logger,
	}
}

// SetAliasStore attaches a persistence store for alias entries.
// Must be called before LoadAliases.
func (r *Registry) SetAliasStore(store *AliasStore) {
	r.aliasStore = store
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

// RegisterAlias records an alias entry (volume handle → disk path) and
// persists it to disk so it survives daemon restarts.
func (r *Registry) RegisterAlias(key, localPath string) {
	r.mu.Lock()
	r.entries[key] = localPath
	r.aliases[key] = localPath
	r.mu.Unlock()

	r.logger.Debug("registered-alias", lager.Data{"key": key, "path": localPath})
	r.persistAliases()
}

// LoadAliases reads persisted aliases from the AliasStore and merges them
// into the registry. Stale entries (path no longer exists) are skipped by
// the store's Load method.
func (r *Registry) LoadAliases() error {
	if r.aliasStore == nil {
		return nil
	}

	loaded, err := r.aliasStore.Load()
	if err != nil {
		return err
	}

	r.mu.Lock()
	for key, path := range loaded {
		r.entries[key] = path
		r.aliases[key] = path
	}
	r.mu.Unlock()

	if len(loaded) > 0 {
		r.logger.Info("aliases-restored", lager.Data{"count": len(loaded)})
	}
	return nil
}

// Remove deletes a key from the registry and alias store.
func (r *Registry) Remove(key string) {
	r.mu.Lock()
	delete(r.entries, key)
	_, wasAlias := r.aliases[key]
	delete(r.aliases, key)
	r.mu.Unlock()

	if wasAlias {
		r.persistAliases()
	}
}

// RemoveByPath removes all entries whose disk path is under dirPath.
// Used by the sweeper to clean up aliases when a step directory is removed.
func (r *Registry) RemoveByPath(dirPath string) {
	r.mu.Lock()
	hadAliases := false
	for key, path := range r.entries {
		if strings.HasPrefix(path, dirPath) {
			delete(r.entries, key)
			if _, ok := r.aliases[key]; ok {
				delete(r.aliases, key)
				hadAliases = true
			}
		}
	}
	r.mu.Unlock()

	if hadAliases {
		r.persistAliases()
	}
}

// persistAliases writes the current alias map to the AliasStore. Errors are
// logged but not propagated — persistence failure should not break registration.
func (r *Registry) persistAliases() {
	if r.aliasStore == nil {
		return
	}

	r.mu.RLock()
	snapshot := make(map[string]string, len(r.aliases))
	for k, v := range r.aliases {
		snapshot[k] = v
	}
	r.mu.RUnlock()

	if err := r.aliasStore.Save(snapshot); err != nil {
		r.logger.Error("failed-to-persist-aliases", err)
	}
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
//	<storagePath>/steps/<handle>/<output>/
//
// Each <handle> directory is registered with key = handle and path = the
// full disk path to the handle directory.
func (r *Registry) ScanHostPath(storagePath string) error {
	stepsDir := filepath.Join(storagePath, "steps")
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
