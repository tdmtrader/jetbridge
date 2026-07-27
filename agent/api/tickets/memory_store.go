package tickets

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workitem"
)

// MemoryStore is an in-memory Store for testing (reviews.MemoryStore
// precedent). It mirrors the DB factory's semantics, including the
// single-writer transition rules.
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	byID   map[int]*Ticket
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[int]*Ticket{}}
}

func (m *MemoryStore) Create(t *Ticket) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	cp := *t
	cp = cloneTicket(cp)
	cp.ID = m.nextID
	cp.Revision = 1
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
	cp := cloneTicket(*t)
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
		out = append(out, cloneTicket(*t))
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
	if upd.UserID != nil {
		v := *upd.UserID
		t.UserID = &v
	}
	if upd.WorkflowName != nil {
		if t.DispatchReservationKey != "" && *upd.WorkflowName != t.WorkflowName {
			return ErrDispatchConflict
		}
		t.WorkflowName = *upd.WorkflowName
	}
	if upd.WorkflowVersion != nil {
		if t.DispatchReservationKey != "" && (t.WorkflowVersion == nil || *upd.WorkflowVersion != *t.WorkflowVersion) {
			return ErrDispatchConflict
		}
		v := *upd.WorkflowVersion
		t.WorkflowVersion = &v
	}
	if upd.TargetBranch != nil {
		t.TargetBranch = *upd.TargetBranch
	}
	if upd.RepositorySnapshotID != nil {
		if err := upd.RepositorySnapshotID.Validate(); err != nil {
			return ErrDispatchConflict
		}
		if t.DispatchReservationKey != "" && t.RepositorySnapshotID != nil && *t.RepositorySnapshotID != *upd.RepositorySnapshotID {
			return ErrDispatchConflict
		}
		v := *upd.RepositorySnapshotID
		t.RepositorySnapshotID = &v
	}
	m.bump(t)
	return nil
}

func (m *MemoryStore) ReserveDispatch(
	ctx context.Context,
	id int,
	request DispatchReservationRequest,
) (DispatchReservation, error) {
	if ctx == nil {
		return DispatchReservation{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return DispatchReservation{}, err
	}
	if request.ExpectedRevision <= 0 || request.WorkflowVersion <= 0 || request.WorkflowDefinitionID <= 0 {
		return DispatchReservation{}, ErrDispatchConflict
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[id]
	if !found {
		return DispatchReservation{}, ErrTicketNotFound
	}
	if ticket.DispatchReservationKey != "" {
		if ticket.WorkflowVersion == nil || *ticket.WorkflowVersion != request.WorkflowVersion ||
			ticket.WorkflowDefinitionID == nil || *ticket.WorkflowDefinitionID != request.WorkflowDefinitionID ||
			(ticket.State != StateQueued && ticket.State != StateRunning) {
			return DispatchReservation{}, ErrDispatchConflict
		}
		return DispatchReservation{Key: ticket.DispatchReservationKey, Ticket: cloneTicket(*ticket)}, nil
	}
	if ticket.State != StateQueued {
		return DispatchReservation{}, ErrStaleTransition
	}
	if ticket.Revision != request.ExpectedRevision {
		return DispatchReservation{}, ErrDispatchConflict
	}
	key := fmt.Sprintf("ticket-dispatch/v1/ticket/%d/attempt/%d/revision/%d", ticket.ID, ticket.AttemptCount, ticket.Revision)
	version, definitionID := request.WorkflowVersion, request.WorkflowDefinitionID
	ticket.WorkflowVersion = &version
	ticket.WorkflowDefinitionID = &definitionID
	ticket.DispatchReservationKey = key
	m.bump(ticket)
	return DispatchReservation{Key: key, Ticket: cloneTicket(*ticket), Created: true}, nil
}

func (m *MemoryStore) RecordDispatchWorkItem(
	ctx context.Context,
	id int,
	reservationKey string,
	expectedRevision int64,
	snapshotID snapshot.SnapshotID,
) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if reservationKey == "" || snapshotID.Validate() != nil {
		return ErrDispatchConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[id]
	if !found {
		return ErrTicketNotFound
	}
	if ticket.DispatchReservationKey != reservationKey || (ticket.State != StateQueued && ticket.State != StateRunning) {
		return ErrDispatchConflict
	}
	if ticket.WorkItemSnapshotID != nil {
		if *ticket.WorkItemSnapshotID != snapshotID {
			return ErrDispatchConflict
		}
		return nil
	}
	if ticket.Revision != expectedRevision {
		return ErrDispatchConflict
	}
	value := snapshotID
	ticket.WorkItemSnapshotID = &value
	m.bump(ticket)
	return nil
}

func (m *MemoryStore) RecordDispatchRun(
	ctx context.Context,
	id int,
	reservationKey string,
	workflowRunID snapshot.WorkflowRunID,
	pipelineRunID int,
) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if reservationKey == "" || workflowRunID.Validate() != nil || pipelineRunID <= 0 {
		return ErrDispatchConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[id]
	if !found {
		return ErrTicketNotFound
	}
	if ticket.DispatchReservationKey != reservationKey || ticket.WorkItemSnapshotID == nil ||
		ticket.RepositorySnapshotID == nil || (ticket.State != StateQueued && ticket.State != StateRunning) {
		return ErrDispatchConflict
	}
	if ticket.WorkflowRunID != nil || ticket.PipelineRunID != nil {
		if ticket.WorkflowRunID == nil || ticket.PipelineRunID == nil ||
			*ticket.WorkflowRunID != workflowRunID || *ticket.PipelineRunID != pipelineRunID {
			return ErrDispatchConflict
		}
		return nil
	}
	workflowValue, pipelineValue := workflowRunID, pipelineRunID
	ticket.WorkflowRunID = &workflowValue
	ticket.PipelineRunID = &pipelineValue
	m.bump(ticket)
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
	m.bump(t)
	switch to {
	case StateDraft:
		t.DispatchReservationKey = ""
		t.WorkflowRunID = nil
		t.WorkItemSnapshotID = nil
		t.PipelineRunID = nil
	case StateQueued:
		t.CompletedAt = 0
		if from == StateRunning {
			// running → queued records a retry attempt.
			t.AttemptCount++
		}
		if from != StateDraft {
			t.DispatchReservationKey = ""
			t.WorkflowRunID = nil
			t.WorkItemSnapshotID = nil
			t.PipelineRunID = nil
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
	case StateClosed:
		t.CompletedAt = time.Now().Unix()
	}
	return nil
}

func (m *MemoryStore) CaptureRevision(ctx context.Context, ticketID int) (workitem.CapturedRevision, bool, error) {
	if ctx == nil {
		return workitem.CapturedRevision{}, false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[ticketID]
	if !found {
		return workitem.CapturedRevision{}, false, nil
	}

	// State and the workflow selection are deliberately absent: work-item/v1
	// freezes authored content only, and both of those belong to the durable
	// run (contracts.WorkItemDocument).
	revision := workitem.Revision{
		TicketID: ticket.ID, Revision: ticket.Revision,
		UpdatedAt: time.Unix(ticket.UpdatedAt, 0).UTC(),
		Adapter:   ticketAdapter(*ticket), ExternalID: ticketExternalID(*ticket),
		Title: ticket.Title, Body: ticket.Body,
	}
	captured, err := workitem.MarshalRevision(revision)
	if err != nil {
		return workitem.CapturedRevision{}, true, err
	}
	return captured, true, nil
}

func (m *MemoryStore) bump(ticket *Ticket) {
	ticket.Revision++
	ticket.UpdatedAt = time.Now().Unix()
}

func ticketAdapter(ticket Ticket) string {
	if ticket.Origin == "jira" {
		return "jira"
	}
	return "jetbridge"
}

func ticketExternalID(ticket Ticket) string {
	if ticket.ExternalRef != "" {
		return ticket.ExternalRef
	}
	return strconv.Itoa(ticket.ID)
}

func cloneTicketInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTicket(ticket Ticket) Ticket {
	ticket.WorkflowVersion = cloneTicketInt(ticket.WorkflowVersion)
	ticket.WorkflowDefinitionID = cloneTicketInt(ticket.WorkflowDefinitionID)
	ticket.UserID = cloneTicketInt(ticket.UserID)
	ticket.PipelineRunID = cloneTicketInt(ticket.PipelineRunID)
	if ticket.WorkflowRunID != nil {
		value := *ticket.WorkflowRunID
		ticket.WorkflowRunID = &value
	}
	if ticket.WorkItemSnapshotID != nil {
		value := *ticket.WorkItemSnapshotID
		ticket.WorkItemSnapshotID = &value
	}
	if ticket.RepositorySnapshotID != nil {
		value := *ticket.RepositorySnapshotID
		ticket.RepositorySnapshotID = &value
	}
	return ticket
}
