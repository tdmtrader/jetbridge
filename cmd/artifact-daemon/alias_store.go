package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"code.cloudfoundry.org/lager/v3"
)

// AliasStore persists volume-handle → disk-path alias mappings to a JSON file
// so they survive daemon restarts. Without this, cache-hit resolution fails
// because the in-memory Registry loses alias entries on restart.
//
// File format: {"vol-handle-1": "steps/container/result", ...}
//
// Values are RelKey — relative to the storage root. Absolute values written by
// earlier versions are still READ and relativized on load, because a node's
// aliases.json survives the upgrade that changes this format and discarding it
// would lose every cache-hit alias on first boot after deploy.
type AliasStore struct {
	path        string // absolute path to aliases.json
	storagePath string
	root        *os.Root // storage root; existence checks go through it
	mu          sync.Mutex
	logger      lager.Logger
}

// NewAliasStore creates a store that persists to <storagePath>/aliases.json.
//
// root may be nil, in which case Load keeps every syntactically valid entry
// instead of dropping the ones whose target is missing.
func NewAliasStore(logger lager.Logger, storagePath string, root *os.Root) *AliasStore {
	return &AliasStore{
		path:        filepath.Join(storagePath, "aliases.json"),
		storagePath: storagePath,
		root:        root,
		logger:      logger,
	}
}

// Save atomically writes the alias map to disk. It writes to a temp file
// first, then renames to avoid corruption on crash.
func (s *AliasStore) Save(aliases map[string]RelKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(aliases, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal aliases: %w", err)
	}

	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename to final path: %w", err)
	}

	return nil
}

// Load reads aliases from disk, relativizes any absolute values left by an
// earlier version, drops anything outside the storage root, and skips entries
// whose target no longer exists. Returns an empty map (not an error) if the
// file doesn't exist yet (first boot).
//
// The existence check goes through the ROOT HANDLE. It used to be os.Stat on
// the stored value; against relative values that resolves against the process
// CWD, so every entry would miss, every entry would be logged as "stale", and
// the whole alias store would be silently emptied on the first boot after
// deploy — the loudest possible consequence behind the quietest possible log
// line. It is also the check a planted absolute symlink would have been
// following.
func (s *AliasStore) Load() (map[string]RelKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]RelKey), nil
		}
		return nil, fmt.Errorf("read aliases file: %w", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal aliases: %w", err)
	}

	valid := make(map[string]RelKey, len(raw))
	refused := 0
	for key, value := range raw {
		rel, err := s.relativize(value)
		if err != nil {
			// Written before containment existed, or planted. Either way it
			// names somewhere outside the store and must not be restored.
			s.logger.Info("alias-refused", lager.Data{"key": key, "value": value, "reason": err.Error()})
			refused++
			continue
		}
		if s.root != nil {
			if _, err := s.root.Stat(osName(rel)); err != nil {
				s.logger.Info("alias-stale", lager.Data{"key": key, "rel": string(rel)})
				continue
			}
		}
		valid[key] = rel
	}

	s.logger.Info("aliases-loaded", lager.Data{
		"total":   len(raw),
		"valid":   len(valid),
		"stale":   len(raw) - len(valid) - refused,
		"refused": refused,
	})

	return valid, nil
}

// relativize accepts both the current relative format and the absolute one
// written by earlier versions.
func (s *AliasStore) relativize(value string) (RelKey, error) {
	if value == "" {
		return "", fmt.Errorf("value is empty")
	}
	if filepath.IsAbs(value) {
		return containedRelKey(s.storagePath, value)
	}
	// Already relative. Run it through the same validator by re-joining, so a
	// persisted "../../etc" is refused rather than trusted for being short.
	return containedRelKey(s.storagePath, filepath.Join(s.storagePath, filepath.FromSlash(value)))
}
