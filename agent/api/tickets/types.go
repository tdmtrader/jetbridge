// Package tickets is the ticket-core domain: agent_tickets /
// agent_ticket_specs / agent_ticket_tasks types, the lifecycle state
// machine, and the single-writer Store contract
// (00-shared-contracts.md §1.7 / §2.1 + ticket-core addendum).
package tickets

import "errors"

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
//   - running → queued (retryable platform error OR rejected send_back
//     checkpoint re-dispatch; attempt_count++) — callers: dispatch's
//     retry path and dispatch's run-completion reconciler.
//   - running → needs_review — two writers: harvest (primary) and
//     dispatch's run-completion reconciler (backup/safety net).
//   - needs_review → concluded — TERMINAL, explicit human disposition
//     ONLY: "run finished, human reviewed, no merge intended"
//     (spike/research flows; FLOWS.md §3). Positive sibling of abandoned.
var validTransitions = map[State][]State{
	StateDraft:       {StateQueued, StateAbandoned},
	StateQueued:      {StateRunning, StateDraft, StateAbandoned},
	StateRunning:     {StateQueued, StateNeedsReview, StateFailed, StateErrored},
	StateNeedsReview: {StateMerged, StateMergedWithFixes, StateSentBack, StateAbandoned, StateConcluded, StateQueued},
	StateSentBack:    {StateQueued},
	StateFailed:      {StateQueued},
	StateErrored:     {StateQueued},
}

func ValidTransition(from, to State) bool {
	for _, t := range validTransitions[from] {
		if t == to {
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
	switch s {
	case StateMerged, StateMergedWithFixes, StateAbandoned, StateConcluded:
		return true
	}
	return false
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
	ErrNoActivePlan      = errors.New("ticket has no submitted plan")
	ErrTaskNotFound      = errors.New("plan task not found")
)

// Ticket is the §2.1 API shape (epoch-seconds timestamps).
type Ticket struct {
	ID                   int      `json:"id"`
	Title                string   `json:"title"`
	Body                 string   `json:"body"`
	State                State    `json:"state"`
	Origin               string   `json:"origin"`
	Repo                 string   `json:"repo"`
	TargetBranch         string   `json:"target_branch"`
	WorkflowName         string   `json:"workflow_name"`
	WorkflowVersion      *int     `json:"workflow_version,omitempty"`
	WorkflowDefinitionID *int     `json:"workflow_definition_id,omitempty"`
	BudgetUSD            *float64 `json:"budget_usd,omitempty"`
	UserID               *int     `json:"user_id,omitempty"`
	UserName             string   `json:"user_name"`
	PipelineRunID        *int     `json:"pipeline_run_id,omitempty"`
	Branch               string   `json:"branch"`
	AttemptCount         int      `json:"attempt_count"`
	ErrorDetail          string   `json:"error_detail,omitempty"`
	CreatedBy            string   `json:"created_by,omitempty"`   // audit attribution (addendum)
	ExternalRef          string   `json:"external_ref,omitempty"` // Jira phase-2 seam (addendum)
	CreatedAt            int64    `json:"created_at"`
	UpdatedAt            int64    `json:"updated_at"`
	CompletedAt          int64    `json:"completed_at,omitempty"`
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

// TicketDetail is the GetAgentTicket response and (wave 3) the
// platform-mcp read_ticket payload — contract addendum.
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

	SubmitSpec(ticketID int, spec Spec) (version int, err error)
	SubmitPlan(ticketID int, tasks []Task) (planVersion int, err error)
	UpdateTaskStatus(ticketID int, planVersion, ordering int, status TaskStatus) error
	AppendTaskNote(ticketID int, planVersion, ordering int, note string) error
	// UpdateActiveTask atomically resolves the ACTIVE plan version and
	// applies the status update (plus optional note append) to it in one
	// store operation, serialized against SubmitPlan — the fix for the
	// read-version-then-write TOCTOU that silently lost updates to a
	// superseded plan (agent-review-native #7 finding, 2026-07-17;
	// additive contract amendment). Returns the plan version written.
	UpdateActiveTask(ticketID, ordering int, status TaskStatus, note string) (planVersion int, err error)
	ActivePlan(ticketID int) ([]Task, error)
	LatestSpec(ticketID int) (*Spec, bool, error)
}

// --- HTTP request bodies (also used by the go-concourse client) ---

type CreateRequest struct {
	Title           string   `json:"title"`
	Body            string   `json:"body"`
	Origin          string   `json:"origin,omitempty"` // default "web"; fly sends "fly"
	Repo            string   `json:"repo"`
	TargetBranch    string   `json:"target_branch,omitempty"`
	WorkflowName    string   `json:"workflow_name,omitempty"`
	WorkflowVersion *int     `json:"workflow_version,omitempty"`
	BudgetUSD       *float64 `json:"budget_usd,omitempty"`
	ExternalRef     string   `json:"external_ref,omitempty"`
}

type UpdateRequest struct {
	Title           *string  `json:"title,omitempty"`
	Body            *string  `json:"body,omitempty"`
	BudgetUSD       *float64 `json:"budget_usd,omitempty"`
	WorkflowName    *string  `json:"workflow_name,omitempty"`
	WorkflowVersion *int     `json:"workflow_version,omitempty"`
	TargetBranch    *string  `json:"target_branch,omitempty"`
}

type TransitionRequest struct {
	From          State  `json:"from"`
	To            State  `json:"to"`
	PipelineRunID *int   `json:"pipeline_run_id,omitempty"`
	Branch        string `json:"branch,omitempty"`
	ErrorDetail   string `json:"error_detail,omitempty"`
}

// SpecSubmission mirrors the §3.2 submit_spec tool input.
type SpecSubmission struct {
	Title              string   `json:"title"`
	Body               string   `json:"body"`
	AcceptanceCriteria []string `json:"acceptance_criteria,omitempty"`
	Links              []Link   `json:"links,omitempty"`
}

// PlanSubmission mirrors the §3.2 submit_plan tool input.
type PlanSubmission struct {
	Tasks []PlanTask `json:"tasks"`
}

type PlanTask struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// TaskStatusRequest mirrors the §3.2 update_task_status tool input.
type TaskStatusRequest struct {
	Status TaskStatus `json:"status"`
	Note   string     `json:"note,omitempty"`
}
