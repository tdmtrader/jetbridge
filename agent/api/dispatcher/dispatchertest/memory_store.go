// Package dispatchertest provides the in-memory dispatcher-settings store the
// dispatcher API tests and the atc/api suite run against. It lives outside
// the production package so no test double is compiled into the web binary.
package dispatchertest

import (
	"errors"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/dispatch"
)

// errInvalidMode mirrors what the db factory rejects (its own
// ErrInvalidDispatcherMode) for a mode outside {off,paused,active}.
var errInvalidMode = errors.New("dispatcher mode must be one of off|paused|active")

// MemoryStore is an in-memory dispatcher.Store. It mirrors the db factory's
// contract: SetDispatcherMode validates the mode, and a missing row reads as
// found=false.
type MemoryStore struct {
	mu        sync.Mutex
	set       bool
	mode      string
	updatedAt time.Time
	updatedBy string
}

// NewMemoryStore mirrors a MIGRATED cluster: migration 1773106137 seeds the
// singleton agent_settings row with dispatcher_mode 'off', so the row is
// always there.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		set:       true,
		mode:      dispatch.ModeOff,
		updatedAt: time.Now(),
		updatedBy: "migration",
	}
}

// NewMemoryStoreWithoutRow mirrors the one way the row can be absent: someone
// deleted it. Every reader fails safe to off.
func NewMemoryStoreWithoutRow() *MemoryStore { return &MemoryStore{} }

func (m *MemoryStore) GetDispatcherSetting() (string, time.Time, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		return "", time.Time{}, "", false, nil
	}
	return m.mode, m.updatedAt, m.updatedBy, true, nil
}

func (m *MemoryStore) SetDispatcherMode(mode, updatedBy string) error {
	if !dispatch.ValidMode(mode) {
		return errInvalidMode
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.set = true
	m.mode = mode
	m.updatedBy = updatedBy
	m.updatedAt = time.Now()
	return nil
}
