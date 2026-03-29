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
// File format: {"vol-handle-1": "/var/.../steps/container/result", ...}
type AliasStore struct {
	path   string // absolute path to aliases.json
	mu     sync.Mutex
	logger lager.Logger
}

// NewAliasStore creates a store that persists to <storagePath>/aliases.json.
func NewAliasStore(logger lager.Logger, storagePath string) *AliasStore {
	return &AliasStore{
		path:   filepath.Join(storagePath, "aliases.json"),
		logger: logger,
	}
}

// Save atomically writes the alias map to disk. It writes to a temp file
// first, then renames to avoid corruption on crash.
func (s *AliasStore) Save(aliases map[string]string) error {
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

// Load reads aliases from disk and validates each entry's path still exists.
// Stale entries (path gone) are silently skipped. Returns an empty map (not
// an error) if the file doesn't exist yet (first boot).
func (s *AliasStore) Load() (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("read aliases file: %w", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal aliases: %w", err)
	}

	valid := make(map[string]string, len(raw))
	for key, path := range raw {
		if _, err := os.Stat(path); err != nil {
			s.logger.Info("alias-stale", lager.Data{"key": key, "path": path})
			continue
		}
		valid[key] = path
	}

	s.logger.Info("aliases-loaded", lager.Data{
		"total": len(raw),
		"valid": len(valid),
		"stale": len(raw) - len(valid),
	})

	return valid, nil
}
