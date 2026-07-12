package metrics

import (
	"sort"
	"sync"

	schema "github.com/concourse/concourse/agent/schema"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[[2]any]schema.RunMetrics // key: {buildID, planID}
	seq  int
	ord  map[[2]any]int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[[2]any]schema.RunMetrics{}, ord: map[[2]any]int{}}
}

func (s *MemoryStore) Upsert(rm *schema.RunMetrics) error {
	_, _, err := s.UpsertReturningInserted(rm)
	return err
}

func (s *MemoryStore) UpsertReturningInserted(rm *schema.RunMetrics) (bool, *schema.RunMetrics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]any{rm.BuildID, rm.PlanID}
	var prev *schema.RunMetrics
	if old, existed := s.rows[key]; existed {
		cp := old
		prev = &cp
	} else {
		s.seq++
		s.ord[key] = s.seq
	}
	s.rows[key] = *rm
	return prev == nil, prev, nil
}

func (s *MemoryStore) InsertIfAbsent(rm *schema.RunMetrics) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := [2]any{rm.BuildID, rm.PlanID}
	if _, existed := s.rows[key]; existed {
		return false, nil // never clobber an existing row (F24)
	}
	s.seq++
	s.ord[key] = s.seq
	s.rows[key] = *rm
	return true, nil
}

func (s *MemoryStore) GetByBuild(buildID int) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool { return rm.BuildID == buildID })
}

func (s *MemoryStore) ListByTicket(ticketID int) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool {
		return rm.TicketID != nil && *rm.TicketID == ticketID
	})
}

func (s *MemoryStore) list(match func(schema.RunMetrics) bool) ([]schema.RunMetrics, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type entry struct {
		ord int
		rm  schema.RunMetrics
	}
	var entries []entry
	for key, rm := range s.rows {
		if match(rm) {
			entries = append(entries, entry{s.ord[key], rm})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ord < entries[j].ord })
	out := make([]schema.RunMetrics, len(entries))
	for i, e := range entries {
		out[i] = e.rm
	}
	return out, nil
}
