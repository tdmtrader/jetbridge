// Package tickets is the ticket-core domain: agent_tickets /
// agent_ticket_specs / agent_ticket_tasks types, the lifecycle state
// machine, and the single-writer Store contract
// (00-shared-contracts.md §1.7 / §2.1 + ticket-core addendum).
package tickets

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workitem"
)

type State string

const (
	StateDraft           State = "draft"
	StateQueued          State = "queued"
	StateRunning         State = "running"
	StateNeedsReview     State = "needs_review"
	StateMerged          State = "merged"
	StateMergedWithFixes State = "merged_with_fixes"
	StateSentBack        State = "sent_back"
	StateAbandoned       State = "abandoned"
	// StateConcluded is TERMINAL: run finished, human reviewed, no merge
	// intended (spike/research flows) — the positive sibling of abandoned.
	// In the frozen enum from day one per FLOWS.md §3/§4 (pre-freeze add,
	// 2026-07-09), so it never needs a later migration.
	StateConcluded State = "concluded"
	StateFailed    State = "failed"
	StateErrored   State = "errored"
)

// validTransitions is the §1.7 state machine. Transition (the
// single-writer function) consults it; nothing else writes state.
//
// Edge notes (do not narrow):
//   - running → queued records a retry attempt (attempt_count++).
//   - running → needs_review — two writers: harvest (primary) and
//     dispatch's run-completion reconciler (backup/safety net).
//   - needs_review → concluded — TERMINAL, explicit human disposition
//     ONLY: "run finished, human reviewed, no merge intended"
//     (spike/research flows; FLOWS.md §3). Positive sibling of abandoned.
//   - failed/errored → abandoned — human disposition of a dead ticket.
//     Without this edge the ONLY exit from failed/errored is a paid
//     re-dispatch, so dead tickets accumulate forever in every "active"
//     listing (dashboard strip, queue) with no way to write them off.
var validTransitions = map[State][]State{
	StateDraft:       {StateQueued, StateAbandoned},
	StateQueued:      {StateRunning, StateDraft, StateAbandoned},
	StateRunning:     {StateQueued, StateNeedsReview, StateFailed, StateErrored},
	StateNeedsReview: {StateMerged, StateMergedWithFixes, StateSentBack, StateAbandoned, StateConcluded, StateQueued},
	StateSentBack:    {StateQueued},
	StateFailed:      {StateQueued, StateAbandoned},
	StateErrored:     {StateQueued, StateAbandoned},
}

func ValidTransition(from, to State) bool {
	for _, t := range validTransitions[from] {
		if t == to {
			return true
		}
	}
	return false
}

// TerminalStates returns the states with no outgoing edges in
// validTransitions: the ticket is done and nothing will ever run for it
// again. The pipeline-run lifecycler archives a ticket's pipelines once it
// lands in one of these (C3), so adding a state here hides its dashboard
// cards for good.
func TerminalStates() []State {
	return []State{StateMerged, StateMergedWithFixes, StateAbandoned, StateConcluded}
}

func IsTerminal(s State) bool {
	for _, t := range TerminalStates() {
		if t == s {
			return true
		}
	}
	return false
}

func ValidState(s State) bool {
	if _, ok := validTransitions[s]; ok {
		return true
	}
	// terminal states have no outgoing edges but are still valid
	return IsTerminal(s)
}

func ValidOrigin(o string) bool {
	switch o {
	case "web", "fly", "jira", "retrospective":
		return true
	}
	return false
}

type TaskStatus string

const (
	TaskPending    TaskStatus = "pending"
	TaskInProgress TaskStatus = "in_progress"
	TaskDone       TaskStatus = "done"
	TaskSkipped    TaskStatus = "skipped"
	TaskBlocked    TaskStatus = "blocked"
)

func ValidTaskStatus(s TaskStatus) bool {
	switch s {
	case TaskPending, TaskInProgress, TaskDone, TaskSkipped, TaskBlocked:
		return true
	}
	return false
}

var (
	ErrInvalidTransition = errors.New("invalid ticket state transition")
	ErrTicketNotFound    = errors.New("ticket not found")
	ErrStaleTransition   = errors.New("ticket state changed concurrently")
	// ErrDispatchConflict means a caller attempted to change or reuse an
	// immutable dispatch reservation with different identity or inputs.
	ErrDispatchConflict = errors.New("ticket dispatch reservation conflict")
)

// Ticket is the §2.1 API shape (epoch-seconds timestamps).
type Ticket struct {
	ID                     int                     `json:"id"`
	Revision               int64                   `json:"revision"`
	Title                  string                  `json:"title"`
	Body                   string                  `json:"body"`
	State                  State                   `json:"state"`
	Origin                 string                  `json:"origin"`
	Repo                   string                  `json:"repo"`
	TargetBranch           string                  `json:"target_branch"`
	WorkflowName           string                  `json:"workflow_name"`
	WorkflowVersion        *int                    `json:"workflow_version,omitempty"`
	WorkflowDefinitionID   *int                    `json:"workflow_definition_id,omitempty"`
	BudgetUSD              *float64                `json:"budget_usd,omitempty"`
	UserID                 *int                    `json:"user_id,omitempty"`
	UserName               string                  `json:"user_name"`
	PipelineRunID          *int                    `json:"pipeline_run_id,omitempty"`
	WorkflowRunID          *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	WorkItemSnapshotID     *snapshot.SnapshotID    `json:"work_item_snapshot_id,omitempty"`
	RepositorySnapshotID   *snapshot.SnapshotID    `json:"repository_snapshot_id,omitempty"`
	DispatchReservationKey string                  `json:"dispatch_reservation_key,omitempty"`
	Branch                 string                  `json:"branch"`
	AttemptCount           int                     `json:"attempt_count"`
	ErrorDetail            string                  `json:"error_detail,omitempty"`
	CreatedBy              string                  `json:"created_by,omitempty"`   // audit attribution (addendum)
	ExternalRef            string                  `json:"external_ref,omitempty"` // Jira phase-2 seam (addendum)
	CreatedAt              int64                   `json:"created_at"`
	UpdatedAt              int64                   `json:"updated_at"`
	CompletedAt            int64                   `json:"completed_at,omitempty"`
}

type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

// Spec is one agent_ticket_specs row (structured envelope + markdown body).
type Spec struct {
	ID                 int      `json:"id"`
	TicketID           int      `json:"ticket_id"`
	Version            int      `json:"version"`
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Links              []Link   `json:"links"`
	SubmittedBy        string   `json:"submitted_by"`
	CreatedAt          int64    `json:"created_at"`
}

// Task is one agent_ticket_tasks row.
type Task struct {
	ID          int        `json:"id"`
	TicketID    int        `json:"ticket_id"`
	PlanVersion int        `json:"plan_version"`
	Ordering    int        `json:"ordering"`
	Title       string     `json:"title"`
	Detail      string     `json:"detail,omitempty"`
	Status      TaskStatus `json:"status"`
	UpdatedAt   int64      `json:"updated_at"`
}

// TicketDetail is the GetAgentTicket response payload.
type TicketDetail struct {
	Ticket Ticket `json:"ticket"`
	Spec   *Spec  `json:"spec"`
	Tasks  []Task `json:"tasks"`
}

type ListFilter struct {
	State  State
	Repo   string
	Origin string
	Limit  int
}

// Update is the non-state mutation set (title/body/budget/workflow
// ref/target branch). nil = leave unchanged. State is NEVER here —
// Transition is the only state writer.
type Update struct {
	Title           *string
	Body            *string
	BudgetUSD       *float64
	WorkflowName    *string
	WorkflowVersion *int
	TargetBranch    *string
	// RepositorySnapshotID selects the exact immutable repository value the
	// ticket adapter will bind. Once a dispatch reservation exists, a missing
	// value may be filled exactly once and an existing value is immutable.
	RepositorySnapshotID *snapshot.SnapshotID
	// UserID resolves the triggering user (co-signed dispatch remainder,
	// 2026-07-17): dispatch looks up users.id from UserName at dispatch
	// time and records it here — the wave-4 leg the create handler's
	// comment promised. Additive; nil = leave unchanged.
	UserID *int
}

// DispatchReservationRequest is the compare-and-set input for claiming a
// queued ticket for one exact workflow definition. The store derives the
// idempotency key from the locked ticket row; callers cannot choose it.
type DispatchReservationRequest struct {
	ExpectedRevision     int64
	WorkflowVersion      int
	WorkflowDefinitionID int
}

// DispatchReservation is the durable claim plus the ticket state observed
// under the reservation lock. Created distinguishes a new claim from an
// idempotent resume of the same claim.
type DispatchReservation struct {
	Key     string
	Ticket  Ticket
	Created bool
}

// TransitionMeta carries the side-band values a transition records.
type TransitionMeta struct {
	PipelineRunID *int   // recorded on → running (set by dispatch)
	Branch        string // recorded on → needs_review (harvest, the primary writer; the reconciler backup-writer leaves it empty)
	ErrorDetail   string // recorded on → errored
}

// Store is the single-writer contract. Transition is THE ONLY way any
// code path (API handler, dispatcher — including its run-completion
// reconciler — harvest, outcome watcher, HITL) changes Ticket.State. It enforces the state machine in
// shared-contracts §1.7, records timestamps, and returns
// ErrInvalidTransition otherwise. It uses optimistic concurrency: the
// UPDATE is guarded by the expected `from` state.
//
//counterfeiter:generate . Store
type Store interface {
	Create(t *Ticket) (int, error)
	Get(id int) (*Ticket, bool, error)
	List(filter ListFilter) ([]Ticket, error)
	Update(id int, upd Update) error // title/body/budget/workflow ref; never state
	Transition(id int, from, to State, meta TransitionMeta) error
	ReserveDispatch(context.Context, int, DispatchReservationRequest) (DispatchReservation, error)
	RecordDispatchWorkItem(context.Context, int, string, int64, snapshot.SnapshotID) error
	RecordDispatchRun(context.Context, int, string, snapshot.WorkflowRunID, int) error

	ActivePlan(ticketID int) ([]Task, error)
	LatestSpec(ticketID int) (*Spec, bool, error)

	// CaptureRevision returns strict work-item/v1 bytes assembled from one
	// source snapshot. Implementations must never compose this by calling the
	// ordinary getters independently.
	CaptureRevision(context.Context, int) (workitem.CapturedRevision, bool, error)
}

// --- HTTP request bodies (also used by the go-concourse client) ---

type CreateRequest struct {
	Title                string               `json:"title"`
	Body                 string               `json:"body"`
	Origin               string               `json:"origin,omitempty"` // default "web"; fly sends "fly"
	Repo                 string               `json:"repo"`
	TargetBranch         string               `json:"target_branch,omitempty"`
	WorkflowName         string               `json:"workflow_name,omitempty"`
	WorkflowVersion      *int                 `json:"workflow_version,omitempty"`
	BudgetUSD            *float64             `json:"budget_usd,omitempty"`
	ExternalRef          string               `json:"external_ref,omitempty"`
	RepositorySnapshotID *snapshot.SnapshotID `json:"repository_snapshot_id,omitempty"`
}

type UpdateRequest struct {
	Title                *string              `json:"title,omitempty"`
	Body                 *string              `json:"body,omitempty"`
	BudgetUSD            *float64             `json:"budget_usd,omitempty"`
	WorkflowName         *string              `json:"workflow_name,omitempty"`
	WorkflowVersion      *int                 `json:"workflow_version,omitempty"`
	TargetBranch         *string              `json:"target_branch,omitempty"`
	RepositorySnapshotID *snapshot.SnapshotID `json:"repository_snapshot_id,omitempty"`
}

type TransitionRequest struct {
	From        State  `json:"from"`
	To          State  `json:"to"`
	Branch      string `json:"branch,omitempty"`
	ErrorDetail string `json:"error_detail,omitempty"`
}

// UnmarshalJSON rejects the retired, server-owned pipeline_run_id key
// instead of silently ignoring it. The durable workflow/pipeline link is
// written only by in-process dispatch, never accepted from an HTTP caller
// (F30 id class); a stale client that still sends the key learns its write
// was dropped rather than believing it took effect. Presence alone is the
// rejection — the value (including null) does not matter.
func (r *TransitionRequest) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if _, present := probe["pipeline_run_id"]; present {
		return errors.New("pipeline_run_id is server-owned")
	}
	type alias TransitionRequest
	var decoded alias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = TransitionRequest(decoded)
	return nil
}

// DispatchResponse is the DispatchAgentTicket 201 body (manual-dispatch
// slice, 2026-07-17): the created pipeline run and the per-ticket
// template pipeline it materialized from.
type DispatchResponse struct {
	RunID         int                     `json:"run_id"`
	PipelineName  string                  `json:"pipeline_name"`
	WorkflowRunID *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	// Warnings carries advisory spec-lint findings (ticket #46:
	// vocabulary known to trigger CLI usage-policy false refusals).
	// Additive and omitempty — never a dispatch blocker.
	Warnings []string `json:"warnings,omitempty"`
}
