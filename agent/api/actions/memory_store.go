package actions

import (
	"errors"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/publisher"
)

var errInvalidMode = errors.New("actions mode must be one of active|suppressed")

// MemoryStore is an in-memory Store for tests and the atc/api suite. It mirrors
// the db factory's contract: never set -> found=false; SetActionsMode validates.
type MemoryStore struct {
	mu        sync.Mutex
	set       bool
	mode      string
	updatedAt time.Time
	updatedBy string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (m *MemoryStore) GetActionsSetting() (string, time.Time, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.set {
		return "", time.Time{}, "", false, nil
	}
	return m.mode, m.updatedAt, m.updatedBy, true, nil
}

func (m *MemoryStore) SetActionsMode(mode, updatedBy string) error {
	if !publisher.ValidActionsMode(mode) {
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
