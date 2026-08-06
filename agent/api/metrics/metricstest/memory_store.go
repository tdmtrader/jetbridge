// Package metricstest provides the in-memory agent-run-metrics store the
// metrics API tests and the atc/api suite run against. It lives outside the
// production package so no test double is compiled into the web binary.
package metricstest

import (
	"sort"
	"sync"

	schema "github.com/concourse/concourse/agent/schema"
	"github.com/concourse/concourse/agent/snapshot"
)

// MemoryStore is an in-memory Store for tests.
type MemoryStore struct {
	mu             sync.Mutex
	rows           map[[2]any]schema.RunMetrics // key: {buildID, planID}
	seq            int
	ord            map[[2]any]int
	restartPending map[[2]any]bool
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[[2]any]schema.RunMetrics{},
		restartPending: map[[2]any]bool{}, ord: map[[2]any]int{}}
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

// MarkRestartPending records that the next ingestion of this step is a new
// execution. The memory store keeps the flag but does not model the additive
// merge: the SQL factory owns that behavior and atc/db owns its coverage.
func (s *MemoryStore) MarkRestartPending(buildID int, planID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restartPending[[2]any{buildID, planID}] = true
	return nil
}

func (s *MemoryStore) GetByBuild(buildID int) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool { return rm.BuildID == buildID })
}

func (s *MemoryStore) ListByWorkflowRun(_ string, runID snapshot.WorkflowRunID) ([]schema.RunMetrics, error) {
	return s.list(func(rm schema.RunMetrics) bool {
		// The run id is the identity. The DB join additionally scopes it by
		// the run's workflow name; this double models no agent_workflow_runs
		// table, so it can only match the id. The metric field is the
		// schema-local WorkflowRunID; compare by int64 against the
		// snapshot-typed request id (both are int64 underneath).
		return rm.WorkflowRunID != nil && int64(*rm.WorkflowRunID) == int64(runID)
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

// WorkflowStats has no in-memory answer: workflow name and version live on
// agent_workflow_runs, which the DB factory INNER JOINs and this double does
// not model (the metric row itself carries only the run id). It reports no
// versions rather than inventing an aggregation the real store cannot match.
func (s *MemoryStore) WorkflowStats(string) ([]schema.WorkflowVersionStats, error) {
	return []schema.WorkflowVersionStats{}, nil
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
