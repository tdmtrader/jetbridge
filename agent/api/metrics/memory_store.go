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

// WorkflowStats mirrors the DB factory's aggregation (agent_run_metrics
// GROUP BY workflow_version, LEFT JOIN builds for success truth) over the
// in-memory rows: one row per distinct workflow_version for the named
// workflow, newest version first with the NULL-version bucket last. The
// "run" unit is a distinct build_id; success counts distinct build_ids whose
// stored BuildStatus is "succeeded" (the in-memory stand-in for the builds
// join). WorkflowRuns counts distinct non-nil WorkflowRunIDs — the durable
// execution identity that replaced the retired ticket_id column. Raw counters
// only — callers apply WithDerived for the ratios.
func (s *MemoryStore) WorkflowStats(workflowName string) ([]schema.WorkflowVersionStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	type versionKey struct {
		hasVersion bool
		version    int
	}
	type agg struct {
		version      *int
		builds       map[int]struct{}
		succeeded    map[int]struct{}
		workflowRuns map[int64]struct{}
		totalCostUSD float64
		totalTurns   int
	}
	buckets := map[versionKey]*agg{}

	for _, rm := range s.rows {
		if rm.WorkflowName != workflowName {
			continue
		}
		var key versionKey
		var vptr *int
		if rm.WorkflowVersion != nil {
			key = versionKey{hasVersion: true, version: *rm.WorkflowVersion}
			v := *rm.WorkflowVersion
			vptr = &v
		}
		a := buckets[key]
		if a == nil {
			a = &agg{
				version:      vptr,
				builds:       map[int]struct{}{},
				succeeded:    map[int]struct{}{},
				workflowRuns: map[int64]struct{}{},
			}
			buckets[key] = a
		}
		a.builds[rm.BuildID] = struct{}{}
		if rm.BuildStatus == "succeeded" {
			a.succeeded[rm.BuildID] = struct{}{}
		}
		if rm.WorkflowRunID != nil {
			a.workflowRuns[int64(*rm.WorkflowRunID)] = struct{}{}
		}
		a.totalCostUSD += rm.CostUSD
		a.totalTurns += rm.Turns
	}

	out := make([]schema.WorkflowVersionStats, 0, len(buckets))
	for _, a := range buckets {
		out = append(out, schema.WorkflowVersionStats{
			Version:       a.version,
			Runs:          len(a.builds),
			WorkflowRuns:  len(a.workflowRuns),
			SucceededRuns: len(a.succeeded),
			TotalCostUSD:  a.totalCostUSD,
			TotalTurns:    a.totalTurns,
		})
	}
	// ORDER BY workflow_version DESC NULLS LAST
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version == nil {
			return false
		}
		if out[j].Version == nil {
			return true
		}
		return *out[i].Version > *out[j].Version
	})
	return out, nil
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
