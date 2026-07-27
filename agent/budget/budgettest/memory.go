// Package budgettest provides the in-memory cost ledger the budget tests,
// the costs API tests, and the atc/api suite run against. It lives outside
// the production package so no test double is compiled into the web binary.
package budgettest

import (
	"github.com/concourse/concourse/agent/budget"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MemoryLedger is an in-memory Ledger for tests and the api suite.
type MemoryLedger struct {
	mu      sync.Mutex
	entries []budget.LedgerEntry
}

func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{}
}

func (m *MemoryLedger) Insert(entry budget.LedgerEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now()
	}
	m.entries = append(m.entries, entry)
	return nil
}

func (m *MemoryLedger) SpentSince(since time.Time) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, e := range m.entries {
		if !e.OccurredAt.Before(since) {
			sum += e.CostUSD
		}
	}
	return sum, nil
}

func (m *MemoryLedger) Rollup(groupBy string, since, until time.Time) ([]budget.RollupRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := map[string]*budget.RollupRow{}
	for _, e := range m.entries {
		if e.OccurredAt.Before(since) {
			continue
		}
		if !until.IsZero() && !e.OccurredAt.Before(until) {
			continue
		}
		var key string
		switch groupBy {
		case budget.GroupByUser:
			key = e.UserName
		case budget.GroupByWorkflow:
			// The SQL ledger resolves the run's workflow_name through
			// agent_workflow_runs; this double models no run table, so the
			// run id is the finest key it can honestly produce.
			if e.WorkflowRunID != nil {
				key = strconv.FormatInt(*e.WorkflowRunID, 10)
			}
		case budget.GroupByModel:
			key = e.Model
		case budget.GroupByStep:
			key = e.StepName
		default: // GroupByDay
			key = e.OccurredAt.UTC().Format("2006-01-02")
		}
		row := byKey[key]
		if row == nil {
			row = &budget.RollupRow{Key: key}
			byKey[key] = row
		}
		row.Entries++
		row.InputTokens += e.InputTokens
		row.OutputTokens += e.OutputTokens
		row.Turns += int64(e.Turns)
		row.CostUSD += e.CostUSD
	}
	out := []budget.RollupRow{}
	for _, row := range byKey {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
