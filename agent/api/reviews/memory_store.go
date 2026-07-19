package reviews

import (
	"sort"
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for testing.
type MemoryStore struct {
	mu      sync.Mutex
	records []*StoredReview
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (m *MemoryStore) Upsert(rec *StoredReview) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Store a copy so caller mutation after Upsert can't alter the store.
	cp := *rec
	if cp.CreatedAt == 0 {
		cp.CreatedAt = time.Now().Unix()
	}
	for i, existing := range m.records {
		if existing.BuildID == cp.BuildID && existing.Repo == cp.Repo && existing.CommitSha == cp.CommitSha {
			m.records[i] = &cp
			return nil
		}
	}
	m.records = append(m.records, &cp)
	return nil
}

func (m *MemoryStore) GetByBuild(buildID int) ([]StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []StoredReview{}
	for _, rec := range m.records {
		if rec.BuildID == buildID {
			results = append(results, *rec)
		}
	}
	return results, nil
}

// ListByTicket returns records whose TicketID matches, oldest-first
// (BuildID ascending as the in-memory proxy for created-ascending).
func (m *MemoryStore) ListByTicket(ticketID int) ([]StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []StoredReview
	for _, rec := range m.records {
		if rec.TicketID != nil && *rec.TicketID == ticketID {
			out = append(out, *rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuildID < out[j].BuildID })
	return out, nil
}

func (m *MemoryStore) ListByTeam(team string, filter ListFilter) ([]StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []StoredReview{}
	// Iterate newest-first (reverse insertion order) so Limit keeps the
	// newest N, matching the Postgres implementation's created-descending
	// ordering.
	for i := len(m.records) - 1; i >= 0; i-- {
		rec := m.records[i]
		if rec.TeamName != team {
			continue
		}
		if filter.Pipeline != "" && rec.PipelineName != filter.Pipeline {
			continue
		}
		if filter.Repo != "" && rec.Repo != filter.Repo {
			continue
		}
		results = append(results, *rec)
		if filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}
	}
	return results, nil
}
