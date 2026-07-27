// Package tickets is the ticket-core domain: the agent_tickets type, the
// QUEUE lifecycle state machine, and the single-writer Store contract.
//
// The ticket is a work-item/v1 projection shell — a queue slot, not an
// execution record. Its state answers one question only: where is this work
// item in the human/dispatch queue. What actually happened when it ran (the
// review, the repository-change diff, the cost, the disposition) belongs to
// the durable workflow run and its agent_workflow_outcomes row; the ticket
// never mirrors it.
package tickets

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workitem"
)

type State string

// The queue lifecycle, and nothing else. There is deliberately no
// merged / merged_with_fixes / sent_back / concluded / abandoned /
// failed / errored: those were DISPOSITIONS and OUTCOMES mirrored onto
// the ticket, and they now live only on the workflow run
// (agent_workflow_outcomes). A ticket is drafted, queued, run, put in
// front of a human, and closed.
const (
	StateDraft       State = "draft"
	StateQueued      State = "queued"
	StateRunning     State = "running"
	StateNeedsReview State = "needs_review"
	// StateClosed is TERMINAL and disposition-free: the human is done with
	// this work item. WHY it closed — merged, merged after fixes, dropped,
	// analysis-only — is read from the durable run's outcome, never from
	// the ticket.
	StateClosed State = "closed"
)

// validTransitions is the queue state machine. Transition (the
// single-writer function) consults it; nothing else writes state.
//
// Edge notes (do not narrow):
//   - running → queued records a retry attempt (attempt_count++).
//   - running → needs_review is the ONLY completion edge, written by
//     dispatch's run-completion reconciler however the run ended. A run
//     that failed still needs a human to look at it and decide.
//   - needs_review → closed is the human close action after a run; a run
//     that succeeded and one that died close the same way, and the durable
//     run carries the difference.
//   - needs_review → queued re-queues for another attempt.
//   - draft → closed and queued → closed drop a work item that never ran.
//     Without them the only exit from a mistaken or obsolete ticket would be
//     to pay for a dispatch first.
var validTransitions = map[State][]State{
	StateDraft:       {StateQueued, StateClosed},
	StateQueued:      {StateRunning, StateDraft, StateClosed},
	StateRunning:     {StateQueued, StateNeedsReview},
	StateNeedsReview: {StateQueued, StateClosed},
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
	return []State{StateClosed}
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

// ValidOrigin gates who filed the work item. There is no 'jira': the create
// handler rejected that origin with a 400 from the day it was written ("arrives
// with the phase-2 sync component"), so no row has ever carried it and no
// adapter branch keyed off it was ever taken.
func ValidOrigin(o string) bool {
	switch o {
	case "web", "fly", "retrospective":
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

// Ticket is the API shape (epoch-seconds timestamps).
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
	UserID                 *int                    `json:"user_id,omitempty"`
	UserName               string                  `json:"user_name"`
	PipelineRunID          *int                    `json:"pipeline_run_id,omitempty"`
	WorkflowRunID          *snapshot.WorkflowRunID `json:"workflow_run_id,omitempty"`
	WorkItemSnapshotID     *snapshot.SnapshotID    `json:"work_item_snapshot_id,omitempty"`
	RepositorySnapshotID   *snapshot.SnapshotID    `json:"repository_snapshot_id,omitempty"`
	DispatchReservationKey string                  `json:"dispatch_reservation_key,omitempty"`
	AttemptCount           int                     `json:"attempt_count"`
	CreatedBy              string                  `json:"created_by,omitempty"` // audit attribution (addendum)
	// ExternalRef is the work item's identifier in whatever system filed it —
	// origin-agnostic. Empty means the ticket is native and its own id is its
	// external id.
	ExternalRef string `json:"external_ref,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
}

// TicketDetail is the GetAgentTicket response payload. The ticket's own
// content IS the detail: the separate spec and task-plan tables were an
// agent-authored progress surface whose only writers (the sidecar submit
// routes) are gone, so the markdown body is the single place ticket prose
// lives.
type TicketDetail struct {
	Ticket Ticket `json:"ticket"`
}

type ListFilter struct {
	State  State
	Repo   string
	Origin string
	Limit  int
}

// Update is the non-state mutation set (title/body/workflow ref/target
// branch). nil = leave unchanged. State is NEVER here — Transition is the
// only state writer.
type Update struct {
	Title           *string
	Body            *string
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
	PipelineRunID *int // recorded on → running (set by dispatch)
}

// Store is the single-writer contract. Transition is THE ONLY way any
// code path (API handler, dispatcher — including its run-completion
// reconciler — HITL) changes Ticket.State. It enforces the queue
// state machine, records timestamps, and returns ErrInvalidTransition
// otherwise. It uses optimistic concurrency: the UPDATE is guarded by the
// expected `from` state.
//
//counterfeiter:generate . Store
type Store interface {
	Create(t *Ticket) (int, error)
	Get(id int) (*Ticket, bool, error)
	List(filter ListFilter) ([]Ticket, error)
	Update(id int, upd Update) error // title/body/workflow ref; never state
	Transition(id int, from, to State, meta TransitionMeta) error
	ReserveDispatch(context.Context, int, DispatchReservationRequest) (DispatchReservation, error)
	RecordDispatchWorkItem(context.Context, int, string, int64, snapshot.SnapshotID) error
	RecordDispatchRun(context.Context, int, string, snapshot.WorkflowRunID, int) error

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
	ExternalRef          string               `json:"external_ref,omitempty"`
	RepositorySnapshotID *snapshot.SnapshotID `json:"repository_snapshot_id,omitempty"`
}

type UpdateRequest struct {
	Title                *string              `json:"title,omitempty"`
	Body                 *string              `json:"body,omitempty"`
	WorkflowName         *string              `json:"workflow_name,omitempty"`
	WorkflowVersion      *int                 `json:"workflow_version,omitempty"`
	TargetBranch         *string              `json:"target_branch,omitempty"`
	RepositorySnapshotID *snapshot.SnapshotID `json:"repository_snapshot_id,omitempty"`
}

type TransitionRequest struct {
	From State `json:"from"`
	To   State `json:"to"`
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

// DispatchResponse is the DispatchAgentTicket 201 body. The workflow run IS
// the dispatch identity — it is what the ticket is durably linked to and what
// every view keys on — and it is carried as a quoted decimal. The pipeline run
// is an optional execution-linkage diagnostic and may be absent; no pipeline
// NAME is reported, because a workflow run's execution pipeline is an
// implementation detail of admission, not an address a caller should use.
type DispatchResponse struct {
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id"`
	PipelineRunID *int                   `json:"pipeline_run_id,omitempty"`
	// Warnings carries advisory work-item text-lint findings (vocabulary known
	// to trigger CLI usage-policy false refusals). Additive and omitempty —
	// never a dispatch blocker.
	Warnings []string `json:"warnings,omitempty"`
}
