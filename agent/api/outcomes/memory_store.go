package outcomes

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the in-memory Store used by handler/watcher tests.
type MemoryStore struct {
	mu   sync.Mutex
	rows map[int]*Outcome
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{rows: map[int]*Outcome{}}
}

func (s *MemoryStore) Ensure(o *Outcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.rows[o.TicketID]
	if !ok {
		cp := *o
		cp.MergeState = MergeOpen
		cp.CreatedAt = time.Now().Unix()
		cp.UpdatedAt = cp.CreatedAt
		s.rows[o.TicketID] = &cp
		return nil
	}
	// Re-arm a send-back row so the re-dispatch loop's merge is detected (F6):
	// a sent_back disposition drove this open row to closed_unmerged, but the
	// ticket has cycled sent_back → queued → running → needs_review again.
	reArm := existing.MergeState == ClosedUnmerged && existing.Disposition == DispositionSentBack
	if existing.MergeState == MergeOpen || reArm {
		if reArm {
			existing.MergeState = MergeOpen
			existing.Disposition = ""
			existing.DispositionReason = ""
			existing.DispositionNotes = ""
			existing.DisposedBy = ""
		}
		existing.Branch = o.Branch
		existing.PushedSha = o.PushedSha
		existing.BaseSha = o.BaseSha
		existing.UpdatedAt = time.Now().Unix()
	}
	return nil
}

func (s *MemoryStore) Get(ticketID int) (*Outcome, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return nil, false, nil
	}
	cp := *o
	return &cp, true, nil
}

func (s *MemoryStore) ListOpen() ([]Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Outcome
	for _, o := range s.rows {
		if o.MergeState == MergeOpen {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TicketID < out[j].TicketID })
	return out, nil
}

func (s *MemoryStore) ListTerminal() ([]Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var terminal []Outcome
	for _, outcome := range s.rows {
		if outcome.MergeState != MergeOpen {
			terminal = append(terminal, *outcome)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminal[i].TicketID < terminal[j].TicketID })
	return terminal, nil
}

func (s *MemoryStore) RecordMerge(ticketID int, res MergeResult) error {
	if res.State != Merged && res.State != MergedWithFixes {
		return fmt.Errorf("invalid merge target state %q", res.State)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return ErrOutcomeNotFound
	}
	if o.MergeState != MergeOpen {
		return ErrNotOpen
	}
	o.MergeState = res.State
	o.MergedSha = res.MergedSha
	o.MergedAt = time.Now().Unix()
	o.HumanCommitCount = res.HumanCommitCount
	o.HumanLinesAdded = res.HumanLinesAdded
	o.HumanLinesDeleted = res.HumanLinesDeleted
	o.UpdatedAt = time.Now().Unix()
	return nil
}

func (s *MemoryStore) SetDisposition(ticketID int, d DispositionInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return ErrOutcomeNotFound
	}
	o.Disposition = d.Disposition
	o.DispositionReason = d.Reason
	o.DispositionNotes = d.Notes
	o.DisposedBy = d.By
	if o.MergeState == MergeOpen {
		if d.Disposition == DispositionConcluded {
			// positive terminal: no merge was ever intended (§1.11.1) —
			// the watcher skips merge-detection from here on.
			o.MergeState = MergeConcluded
		} else {
			o.MergeState = ClosedUnmerged
		}
	}
	o.UpdatedAt = time.Now().Unix()
	return nil
}

func (s *MemoryStore) Close(ticketID int, state MergeState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state != ClosedUnmerged && state != MergeConcluded {
		return fmt.Errorf("close: invalid terminal state %q", state)
	}
	o, ok := s.rows[ticketID]
	if !ok || o.MergeState != MergeOpen {
		return nil
	}
	o.MergeState = state
	o.UpdatedAt = time.Now().Unix()
	return nil
}

func (s *MemoryStore) Touch(ticketID int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.rows[ticketID]
	if !ok {
		return ErrOutcomeNotFound
	}
	o.LastCheckedAt = time.Now().Unix()
	return nil
}
