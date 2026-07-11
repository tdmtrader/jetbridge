package budget

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"
)

// MemoryLedger is an in-memory Ledger for tests and the api suite.
type MemoryLedger struct {
	mu      sync.Mutex
	entries []LedgerEntry
}

func NewMemoryLedger() *MemoryLedger {
	return &MemoryLedger{}
}

func (m *MemoryLedger) Insert(entry LedgerEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = time.Now()
	}
	m.entries = append(m.entries, entry)
	return nil
}

func (m *MemoryLedger) SpentForTicket(ticketID int) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var sum float64
	for _, e := range m.entries {
		// harvest_judge spend never depletes the ticket budget (§1.13).
		if e.TicketID != nil && *e.TicketID == ticketID && e.Source != SourceHarvestJudge {
			sum += e.CostUSD
		}
	}
	return sum, nil
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

func (m *MemoryLedger) Rollup(groupBy string, since, until time.Time) ([]RollupRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	byKey := map[string]*RollupRow{}
	for _, e := range m.entries {
		if e.OccurredAt.Before(since) {
			continue
		}
		if !until.IsZero() && !e.OccurredAt.Before(until) {
			continue
		}
		var key string
		switch groupBy {
		case GroupByUser:
			key = e.UserName
		case GroupByTicket:
			if e.TicketID != nil {
				key = strconv.Itoa(*e.TicketID)
			}
		case GroupByWorkflow:
			var meta struct {
				Workflow string `json:"workflow"`
			}
			if len(e.Metadata) > 0 {
				_ = json.Unmarshal(e.Metadata, &meta)
			}
			key = meta.Workflow
		default: // GroupByDay
			key = e.OccurredAt.UTC().Format("2006-01-02")
		}
		row := byKey[key]
		if row == nil {
			row = &RollupRow{Key: key}
			byKey[key] = row
		}
		row.Entries++
		row.InputTokens += e.InputTokens
		row.OutputTokens += e.OutputTokens
		row.Turns += int64(e.Turns)
		row.CostUSD += e.CostUSD
	}
	out := []RollupRow{}
	for _, row := range byKey {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
