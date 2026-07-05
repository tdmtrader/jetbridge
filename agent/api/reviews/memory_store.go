package reviews

import "sync"

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
	for i, existing := range m.records {
		if existing.BuildID == rec.BuildID && existing.Repo == rec.Repo && existing.CommitSha == rec.CommitSha {
			m.records[i] = rec
			return nil
		}
	}
	m.records = append(m.records, rec)
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

func (m *MemoryStore) ListByTeam(team string, filter ListFilter) ([]StoredReview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := []StoredReview{}
	for _, rec := range m.records {
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
