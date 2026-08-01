// Package ticketstest provides the in-memory ticket store the tickets API
// tests, the dispatch tests, and the atc/api suite run against. It lives
// outside the production package so no test double is compiled into the web
// binary.
package ticketstest

import (
	"context"
	"fmt"
	"github.com/concourse/concourse/agent/api/tickets"
	"strconv"
	"sync"
	"time"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workitem"
)

// MemoryStore is an in-memory tickets.Store (reviewstest.MemoryStore
// precedent). It mirrors the DB factory's semantics, including the
// single-writer transition rules.
type MemoryStore struct {
	mu     sync.Mutex
	nextID int
	byID   map[int]*tickets.Ticket
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{byID: map[int]*tickets.Ticket{}}
}

func (m *MemoryStore) Create(t *tickets.Ticket) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	cp := *t
	cp = cloneTicket(cp)
	cp.ID = m.nextID
	cp.Revision = 1
	cp.State = tickets.StateDraft
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

func (m *MemoryStore) Get(id int) (*tickets.Ticket, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return nil, false, nil
	}
	cp := cloneTicket(*t)
	return &cp, true, nil
}

func (m *MemoryStore) List(filter tickets.ListFilter) ([]tickets.Ticket, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []tickets.Ticket{}
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

func (m *MemoryStore) Update(id int, upd tickets.Update) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return tickets.ErrTicketNotFound
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
			return tickets.ErrDispatchConflict
		}
		t.WorkflowName = *upd.WorkflowName
	}
	if upd.WorkflowVersion != nil {
		if t.DispatchReservationKey != "" && (t.WorkflowVersion == nil || *upd.WorkflowVersion != *t.WorkflowVersion) {
			return tickets.ErrDispatchConflict
		}
		v := *upd.WorkflowVersion
		t.WorkflowVersion = &v
	}
	if upd.TargetBranch != nil {
		t.TargetBranch = *upd.TargetBranch
	}
	if upd.RepositorySnapshotID != nil {
		if err := upd.RepositorySnapshotID.Validate(); err != nil {
			return tickets.ErrDispatchConflict
		}
		if t.DispatchReservationKey != "" && t.RepositorySnapshotID != nil && *t.RepositorySnapshotID != *upd.RepositorySnapshotID {
			return tickets.ErrDispatchConflict
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
	request tickets.DispatchReservationRequest,
) (tickets.DispatchReservation, error) {
	if ctx == nil {
		return tickets.DispatchReservation{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return tickets.DispatchReservation{}, err
	}
	if request.ExpectedRevision <= 0 || request.WorkflowVersion <= 0 || request.WorkflowDefinitionID <= 0 {
		return tickets.DispatchReservation{}, tickets.ErrDispatchConflict
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[id]
	if !found {
		return tickets.DispatchReservation{}, tickets.ErrTicketNotFound
	}
	if ticket.DispatchReservationKey != "" {
		if ticket.WorkflowVersion == nil || *ticket.WorkflowVersion != request.WorkflowVersion ||
			ticket.WorkflowDefinitionID == nil || *ticket.WorkflowDefinitionID != request.WorkflowDefinitionID ||
			(ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
			return tickets.DispatchReservation{}, tickets.ErrDispatchConflict
		}
		return tickets.DispatchReservation{Key: ticket.DispatchReservationKey, Ticket: cloneTicket(*ticket)}, nil
	}
	if ticket.State != tickets.StateQueued {
		return tickets.DispatchReservation{}, tickets.ErrStaleTransition
	}
	if ticket.Revision != request.ExpectedRevision {
		return tickets.DispatchReservation{}, tickets.ErrDispatchConflict
	}
	key := fmt.Sprintf("ticket-dispatch/v1/ticket/%d/attempt/%d/revision/%d", ticket.ID, ticket.AttemptCount, ticket.Revision)
	version, definitionID := request.WorkflowVersion, request.WorkflowDefinitionID
	ticket.WorkflowVersion = &version
	ticket.WorkflowDefinitionID = &definitionID
	ticket.DispatchReservationKey = key
	m.bump(ticket)
	return tickets.DispatchReservation{Key: key, Ticket: cloneTicket(*ticket), Created: true}, nil
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
		return tickets.ErrDispatchConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[id]
	if !found {
		return tickets.ErrTicketNotFound
	}
	if ticket.DispatchReservationKey != reservationKey || (ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
		return tickets.ErrDispatchConflict
	}
	if ticket.WorkItemSnapshotID != nil {
		if *ticket.WorkItemSnapshotID != snapshotID {
			return tickets.ErrDispatchConflict
		}
		return nil
	}
	if ticket.Revision != expectedRevision {
		return tickets.ErrDispatchConflict
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
		return tickets.ErrDispatchConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ticket, found := m.byID[id]
	if !found {
		return tickets.ErrTicketNotFound
	}
	if ticket.DispatchReservationKey != reservationKey || ticket.WorkItemSnapshotID == nil ||
		ticket.RepositorySnapshotID == nil || (ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
		return tickets.ErrDispatchConflict
	}
	// The durable store derives WorkflowRunID from the reservation the binder
	// admitted the run under, so it is already populated by the time this call
	// arrives and only the execution linkage is written. This fake has no run
	// store to derive from, so it materialises the same identity here; what it
	// reproduces exactly is the conflict contract.
	if ticket.WorkflowRunID != nil && *ticket.WorkflowRunID != workflowRunID {
		return tickets.ErrDispatchConflict
	}
	if ticket.PipelineRunID != nil {
		if *ticket.PipelineRunID != pipelineRunID {
			return tickets.ErrDispatchConflict
		}
		return nil
	}
	workflowValue, pipelineValue := workflowRunID, pipelineRunID
	ticket.WorkflowRunID = &workflowValue
	ticket.PipelineRunID = &pipelineValue
	m.bump(ticket)
	return nil
}

func (m *MemoryStore) Transition(id int, from, to tickets.State, meta tickets.TransitionMeta) error {
	if !tickets.ValidTransition(from, to) {
		return tickets.ErrInvalidTransition
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.byID[id]
	if !ok {
		return tickets.ErrTicketNotFound
	}
	if t.State != from {
		return tickets.ErrStaleTransition
	}
	t.State = to
	m.bump(t)
	switch to {
	case tickets.StateDraft:
		t.DispatchReservationKey = ""
		t.WorkflowRunID = nil
		t.WorkItemSnapshotID = nil
		t.PipelineRunID = nil
	case tickets.StateQueued:
		t.CompletedAt = 0
		if from == tickets.StateRunning {
			// running → queued records a retry attempt.
			t.AttemptCount++
		}
		if from != tickets.StateDraft {
			t.DispatchReservationKey = ""
			t.WorkflowRunID = nil
			t.WorkItemSnapshotID = nil
			t.PipelineRunID = nil
		}
	case tickets.StateRunning:
		if meta.PipelineRunID != nil {
			v := *meta.PipelineRunID
			t.PipelineRunID = &v
		}
	case tickets.StateClosed:
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

	// The ticket state and the workflow selection are deliberately absent: work-item/v1
	// freezes authored content only, and both of those belong to the durable
	// run (contracts.WorkItemDocument).
	revision := workitem.Revision{
		TicketID: ticket.ID, Revision: ticket.Revision,
		UpdatedAt: time.Unix(ticket.UpdatedAt, 0).UTC(),
		Adapter:   "jetbridge", ExternalID: ticketExternalID(*ticket),
		Title: ticket.Title, Body: ticket.Body,
	}
	captured, err := workitem.MarshalRevision(revision)
	if err != nil {
		return workitem.CapturedRevision{}, true, err
	}
	return captured, true, nil
}

func (m *MemoryStore) bump(ticket *tickets.Ticket) {
	ticket.Revision++
	ticket.UpdatedAt = time.Now().Unix()
}

func ticketExternalID(ticket tickets.Ticket) string {
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

func cloneTicket(ticket tickets.Ticket) tickets.Ticket {
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
