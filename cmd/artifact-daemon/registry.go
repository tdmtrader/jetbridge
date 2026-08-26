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
// Only alias entries are persisted (via AliasStore); scan entries are always
// recoverable from the directory structure.
//
// Values are RelKey, relative to storagePath. The registry is the daemon's only
// source of ambient paths, so it is the one place the boundary can attach to
// the value itself rather than be re-derived by every consumer. An entry that
// will not relativize is REFUSED at Register, not stored and re-checked later.
type Registry struct {
	mu sync.RWMutex
	// storagePath is the root every value is relative to. It is the only
	// absolute path the registry holds.
	storagePath string
	entries     map[string]RelKey // key → location under storagePath (all entries)
	aliases     map[string]RelKey // key → location under storagePath (aliases only, persisted)
	aliasStore  *AliasStore       // optional persistence; nil disables persistence
	logger      lager.Logger
}

// NewRegistry creates an empty Registry rooted at storagePath.
func NewRegistry(logger lager.Logger, storagePath string) *Registry {
	return &Registry{
		storagePath: storagePath,
		entries:     make(map[string]RelKey),
		aliases:     make(map[string]RelKey),
		logger:      logger,
	}
}

// StoragePath returns the root all values are relative to.
func (r *Registry) StoragePath() string {
	return r.storagePath
}

// SetAliasStore attaches a persistence store for alias entries.
// Must be called before LoadAliases.
func (r *Registry) SetAliasStore(store *AliasStore) {
	r.aliasStore = store
}

// Register records that artifact `key` is stored at `localPath`, which must lie
// within the storage root.
//
// REFUSING here is the point of the change. Previously any absolute path could
// be stored and the escape was caught — or not — at each of the places that
// later used it. A path that will not relativize never enters the map, so no
// consumer has to be trusted to re-check it.
// It returns the stored location so callers that need it do not re-derive it —
// re-deriving is how the guard key and the stored value drifted apart before.
func (r *Registry) Register(key, localPath string) (RelKey, error) {
	rk, err := containedRelKey(r.storagePath, localPath)
	if err != nil {
		r.logger.Info("register-refused", lager.Data{
			"key": key, "path": localPath, "reason": err.Error(),
		})
		return "", refused("registry: %s", err)
	}

	r.mu.Lock()
	r.entries[key] = rk
	r.mu.Unlock()
	r.logger.Debug("registered", lager.Data{"key": key, "rel": string(rk)})
	return rk, nil
}

// Lookup returns the artifact's location relative to the storage root, or
// ("", false) if not found.
func (r *Registry) Lookup(key string) (RelKey, bool) {
	r.mu.RLock()
	rel, ok := r.entries[key]
	r.mu.RUnlock()
	return rel, ok
}

// AmbientPath re-joins a RelKey to the storage root, producing a path OUTSIDE
// the root handle and therefore outside the containment the handle provides.
//
// Named for what it costs. The three remaining callers are two response-JSON
// fields the ATC acts on and lookupRegistry's use-time check; anything else
// should take a handle.
func (r *Registry) AmbientPath(rel RelKey) string {
	return filepath.Join(r.storagePath, filepath.FromSlash(string(rel)))
}

// RegisterAlias records an alias entry (volume handle → location) and
// persists it to disk so it survives daemon restarts.
func (r *Registry) RegisterAlias(key, localPath string) (RelKey, error) {
	rk, err := containedRelKey(r.storagePath, localPath)
	if err != nil {
		r.logger.Info("register-alias-refused", lager.Data{
			"key": key, "path": localPath, "reason": err.Error(),
		})
		return "", refused("registry: %s", err)
	}

	r.mu.Lock()
	r.entries[key] = rk
	r.aliases[key] = rk
	r.mu.Unlock()

	r.logger.Debug("registered-alias", lager.Data{"key": key, "rel": string(rk)})
	r.persistAliases()
	return rk, nil
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
	for key, rel := range loaded {
		r.entries[key] = rel
		r.aliases[key] = rel
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

// RemoveByPath removes all entries at or under dirPath. Used by the sweeper to
// clean up aliases when a step directory is removed.
//
// The comparison is by PATH SEGMENT, not by string prefix. strings.HasPrefix
// made sweeping .../steps/build-4 also evict build-42, build-4a and every other
// sibling sharing those characters — the artifact stayed on disk and became
// permanently unfindable, presenting as a cache miss rather than as a bug.
func (r *Registry) RemoveByPath(dirPath string) {
	dir, err := containedRelKey(r.storagePath, dirPath)
	if err != nil {
		// Outside the root: nothing stored can be under it, because Register
		// refuses anything that is not. Logged rather than returned in
		// silence — this is the alias-leak path, and every other refusal
		// in this file logs.
		r.logger.Info("remove-by-path-refused", lager.Data{
			"path": dirPath, "reason": err.Error(),
		})
		return
	}

	r.mu.Lock()
	hadAliases := false
	for key, rel := range r.entries {
		if rel != dir && !strings.HasPrefix(string(rel), string(dir)+"/") {
			continue
		}
		delete(r.entries, key)
		if _, ok := r.aliases[key]; ok {
			delete(r.aliases, key)
			hadAliases = true
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
	snapshot := make(map[string]RelKey, len(r.aliases))
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
// Each output is registered under "<handle>/<output>".
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
			if _, err := r.Register(handle+"/"+output.Name(), outputPath); err != nil {
				// The scan walks the store's own tree, so a refusal here means
				// a directory resolves outside it. Skip it, and do NOT count
				// it: this line previously dropped the error and incremented
				// anyway, so the scan reported entries it had not registered —
				// Len()=0 under a log line saying registered=2. A discarded
				// (T, error) call is legal Go, so neither the compiler nor vet
				// could see it.
				r.logger.Error("scan-register-refused", err, lager.Data{
					"handle": handle, "output": output.Name(),
				})
				continue
			}
			registered++
		}
	}

	r.logger.Info("scan-complete", lager.Data{
		"steps_dir":  stepsDir,
		"registered": registered,
	})
	return nil
}
