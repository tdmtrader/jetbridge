package dispatcher

import (
	"sync"
	"time"

	"github.com/concourse/concourse/agent/dispatch"
)

// MemoryStore is an in-memory Store for tests. It mirrors the db factory's
// contract: no row -> found=false; SetDispatcherMode validates the mode.
type MemoryStore struct {
	mu        sync.Mutex
	set       bool
	mode      string
	updatedAt time.Time
	updatedBy string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

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
