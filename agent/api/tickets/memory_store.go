package tickets

import (
	"sync"
	"time"
)

// MemoryStore is an in-memory Store for testing (reviews.MemoryStore
// precedent). It mirrors the DB factory's semantics, including the
// single-writer transition rules.
type MemoryStore struct {
	mu      sync.Mutex
	nextID  int
	byID    map[int]*Ticket
	specs   map[int][]Spec // keyed by ticket id, ascending version
	tasks   map[int][]Task // keyed by ticket id, all plan versions
	taskSeq int
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[int]*Ticket{}, specs: map[int][]Spec{}, tasks: map[int][]Task{}}
}

func (m *MemoryStore) Create(t *Ticket) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	cp := *t
	cp.ID = m.nextID
	cp.State = StateDraft
	if cp.Origin == "" {
		cp.Origin = "web"
	}
	if cp.TargetBranch == "" {
		cp.TargetBranch = "main"
	}
	now := time.Now().Unix()
	cp.CreatedAt, cp.UpdatedAt = now, now
	m.byID[cp.ID] = &cp
	return cp.ID, nil
}

func (m *MemoryStore) Get(id int) (*Ticket, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return nil, false, nil
	}
	cp := *t
	return &cp, true, nil
}

func (m *MemoryStore) List(filter ListFilter) ([]Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Ticket{}
	// newest-first by id (ids are monotonic here)
	for id := m.nextID; id >= 1; id-- {
		t, ok := m.byID[id]
		if !ok {
			continue
		}
		if filter.State != "" && t.State != filter.State {
			continue
		}
		if filter.Repo != "" && t.Repo != filter.Repo {
			continue
		}
		if filter.Origin != "" && t.Origin != filter.Origin {
			continue
		}
		out = append(out, *t)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func (m *MemoryStore) Update(id int, upd Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return ErrTicketNotFound
	}
	if upd.Title != nil {
		t.Title = *upd.Title
	}
	if upd.Body != nil {
		t.Body = *upd.Body
	}
	if upd.BudgetUSD != nil {
		v := *upd.BudgetUSD
		t.BudgetUSD = &v
	}
	if upd.WorkflowName != nil {
		t.WorkflowName = *upd.WorkflowName
	}
	if upd.WorkflowVersion != nil {
		v := *upd.WorkflowVersion
		t.WorkflowVersion = &v
	}
	if upd.TargetBranch != nil {
		t.TargetBranch = *upd.TargetBranch
	}
	t.UpdatedAt = time.Now().Unix()
	return nil
}

func (m *MemoryStore) Transition(id int, from, to State, meta TransitionMeta) error {
	if !ValidTransition(from, to) {
		return ErrInvalidTransition
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return ErrTicketNotFound
	}
	if t.State != from {
		return ErrStaleTransition
	}
	t.State = to
	t.UpdatedAt = time.Now().Unix()
	switch to {
	case StateQueued:
		t.CompletedAt = 0
		if from == StateRunning {
			// running → queued (retryable platform error OR rejected
			// send_back checkpoint re-dispatch; attempt_count++).
			t.AttemptCount++
		}
	case StateRunning:
		if meta.PipelineRunID != nil {
			v := *meta.PipelineRunID
			t.PipelineRunID = &v
		}
	case StateNeedsReview:
		if meta.Branch != "" {
			t.Branch = meta.Branch
		}
	case StateMerged, StateMergedWithFixes, StateSentBack, StateAbandoned, StateConcluded, StateFailed, StateErrored:
		t.CompletedAt = time.Now().Unix()
		if to == StateErrored {
			t.ErrorDetail = meta.ErrorDetail
		}
	}
	return nil
}

func (m *MemoryStore) SubmitSpec(ticketID int, spec Spec) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[ticketID]; !ok {
		return 0, ErrTicketNotFound
	}
	version := len(m.specs[ticketID]) + 1
	spec.ID = version
	spec.TicketID = ticketID
	spec.Version = version
	spec.CreatedAt = time.Now().Unix()
	if spec.AcceptanceCriteria == nil {
		spec.AcceptanceCriteria = []string{}
	}
	if spec.Links == nil {
		spec.Links = []Link{}
	}
	m.specs[ticketID] = append(m.specs[ticketID], spec)
	return version, nil
}

func (m *MemoryStore) LatestSpec(ticketID int) (*Spec, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	specs := m.specs[ticketID]
	if len(specs) == 0 {
		return nil, false, nil
	}
	cp := specs[len(specs)-1]
	return &cp, true, nil
}

func (m *MemoryStore) SubmitPlan(ticketID int, ts []Task) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[ticketID]; !ok {
		return 0, ErrTicketNotFound
	}
	maxVersion := 0
	for _, existing := range m.tasks[ticketID] {
		if existing.PlanVersion > maxVersion {
			maxVersion = existing.PlanVersion
		}
	}
	planVersion := maxVersion + 1
	for i, task := range ts {
		m.taskSeq++
		task.ID = m.taskSeq
		task.TicketID = ticketID
		task.PlanVersion = planVersion
		task.Ordering = i + 1
		if task.Status == "" {
			task.Status = TaskPending
		}
		task.UpdatedAt = time.Now().Unix()
		m.tasks[ticketID] = append(m.tasks[ticketID], task)
	}
	return planVersion, nil
}

func (m *MemoryStore) ActivePlan(ticketID int) ([]Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	maxVersion := 0
	for _, t := range m.tasks[ticketID] {
		if t.PlanVersion > maxVersion {
			maxVersion = t.PlanVersion
		}
	}
	out := []Task{}
	for _, t := range m.tasks[ticketID] {
		if t.PlanVersion == maxVersion {
			out = append(out, t)
		}
	}
	return out, nil
}

func (m *MemoryStore) UpdateTaskStatus(ticketID int, planVersion, ordering int, status TaskStatus) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.tasks[ticketID] {
		if t.PlanVersion == planVersion && t.Ordering == ordering {
			m.tasks[ticketID][i].Status = status
			m.tasks[ticketID][i].UpdatedAt = time.Now().Unix()
			return nil
		}
	}
	return ErrTaskNotFound
}

func (m *MemoryStore) AppendTaskNote(ticketID int, planVersion, ordering int, note string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, t := range m.tasks[ticketID] {
		if t.PlanVersion == planVersion && t.Ordering == ordering {
			if t.Detail == "" {
				m.tasks[ticketID][i].Detail = "> " + note
			} else {
				m.tasks[ticketID][i].Detail = t.Detail + "\n\n> " + note
			}
			m.tasks[ticketID][i].UpdatedAt = time.Now().Unix()
			return nil
		}
	}
	return ErrTaskNotFound
}
