package credentials

import (
	"sync"
	"time"
)

// MemoryBackend is an in-memory Backend for tests.
type MemoryBackend struct {
	mu    sync.Mutex
	users map[string]memUser // sub -> user
	creds map[int]map[string]memCred
}

type memUser struct {
	id   int
	name string
}

type memCred struct {
	token     string
	expiresAt time.Time
	userName  string
}

func NewMemoryBackend() *MemoryBackend {
	return &MemoryBackend{
		users: map[string]memUser{},
		creds: map[int]map[string]memCred{},
	}
}

// AddUser registers a fake users row (login-created in production).
func (m *MemoryBackend) AddUser(sub string, id int, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[sub] = memUser{id: id, name: name}
}

func (m *MemoryBackend) UserBySub(sub string) (int, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[sub]
	return u.id, u.name, ok, nil
}

func (m *MemoryBackend) Put(userID int, userName, kind, token string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.creds[userID] == nil {
		m.creds[userID] = map[string]memCred{}
	}
	m.creds[userID][kind] = memCred{token: token, expiresAt: expiresAt, userName: userName}
	return nil
}

func (m *MemoryBackend) Status(userID int) ([]Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Credential{}
	for kind, c := range m.creds[userID] {
		out = append(out, m.toCredential(userID, kind, c, false))
	}
	return out, nil
}

func (m *MemoryBackend) Resolve(userID int, kind string) (*Credential, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.creds[userID][kind]
	if !ok {
		return nil, false, nil
	}
	cred := m.toCredential(userID, kind, c, true)
	return &cred, true, nil
}

func (m *MemoryBackend) ExpiringWithin(d time.Duration) ([]Credential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cutoff := time.Now().Add(d)
	out := []Credential{}
	for userID, kinds := range m.creds {
		for kind, c := range kinds {
			if !c.expiresAt.IsZero() && c.expiresAt.Before(cutoff) {
				out = append(out, m.toCredential(userID, kind, c, false))
			}
		}
	}
	return out, nil
}

func (m *MemoryBackend) Delete(userID int, kind string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.creds[userID], kind)
	return nil
}

func (m *MemoryBackend) toCredential(userID int, kind string, c memCred, withToken bool) Credential {
	cred := Credential{
		UserID:   userID,
		UserName: c.userName,
		Kind:     kind,
	}
	if !c.expiresAt.IsZero() {
		cred.ExpiresAt = c.expiresAt.Unix()
	}
	if withToken {
		cred.Token = c.token
	}
	return cred
}
