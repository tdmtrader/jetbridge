package metrics

import (
	"sort"
	"sync"

	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
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

func (s *MemoryStore) ListByWorkflowRun(workflowName string, runID snapshot.WorkflowRunID) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool {
		// mirror the DB join: the run id is the identity, scoped by the
		// workflow name (a run id under the wrong workflow name matches nothing).
		// The metric field is the schema-local WorkflowRunID; compare by int64
		// against the snapshot-typed request id (both are int64 underneath).
		return rm.WorkflowName == workflowName && rm.WorkflowRunID != nil && int64(*rm.WorkflowRunID) == int64(runID)
	})
}

func (s *MemoryStore) ListRecent(limit int) ([]schema.RunMetrics, error) {
	all, _ := s.list(func(schema.RunMetrics) bool { return true })
	// list() returns oldest-first; reverse to newest-first.
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if limit > 0 && limit < len(all) {
		all = all[:limit]
	}
	return all, nil
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
		// mirror the DB factory's read path: rows leave the store with the
		// U3 fusion applied
		out[i].Outcome = out[i].DeriveOutcome()
	}
	return out, nil
}
