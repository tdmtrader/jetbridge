package ticketjournal

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/atc/db"
)

// journalPageLimit bounds one journal read. A ticket that has driven more runs
// than this is pathological; the list is deliberately not paginated, because a
// journal is read as a whole story and a cursor would invite the caller to
// reassemble it out of order.
const journalPageLimit = 500

// RunStore is the narrow read surface the journal needs. The production
// db.AgentWorkflowRunsFactory satisfies it.
//
// One method, called once: the journal is ONE query ordered by durable run
// occurrence time, never one query per workflow name.
type RunStore interface {
	List(context.Context, db.AgentWorkflowRunListFilter) ([]db.AgentWorkflowRun, error)
}

// TicketStore is the ticket-existence read. The journal lives in its own
// package because agent/api/tickets must not import atc/db: atc/db imports
// agent/api/tickets for the ticket factory, and the journal reads durable
// workflow runs.
type TicketStore interface {
	Get(id int) (*tickets.Ticket, bool, error)
}

// TrustedTeam is the verified team scope the journal reads within.
type TrustedTeam struct {
	ID int
}

// Handler serves GET /api/v1/agent/tickets/:ticket_id/runs.
type Handler struct {
	tickets TicketStore
	runs    RunStore
	team    TrustedTeam
}

func NewHandler(ticketStore TicketStore, runs RunStore, team TrustedTeam) *Handler {
	return &Handler{tickets: ticketStore, runs: runs, team: team}
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(value)
}

func ticketIDParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.FormValue(":ticket_id"))
	if err != nil || id <= 0 {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// JournalEntry is one run occurrence in a ticket's chronological history.
//
// It is a list entry, not a graph node: there is no edge, parent, or rank
// here, and repeated executions of the same workflow are separate entries.
// RetryOfWorkflowRunID is the run's own durable retry identity, offered so the
// UI can GROUP a retry with its source — it is not a causal edge and nothing
// in the layout depends on it.
type JournalEntry struct {
	WorkflowRunID snapshot.WorkflowRunID `json:"workflow_run_id"`
	WorkflowName  string                 `json:"workflow_name"`
	// WorkflowVersion is the exact revision this occurrence ran, never the
	// promoted one.
	WorkflowVersion       int                                 `json:"workflow_version"`
	DefinitionContentHash string                              `json:"definition_content_hash"`
	Status                db.AgentWorkflowRunStatus           `json:"status"`
	ExecutionStatus       *db.AgentWorkflowRunExecutionStatus `json:"execution_status,omitempty"`
	OriginKind            string                              `json:"origin_kind"`
	OriginReference       string                              `json:"origin_reference,omitempty"`
	RetryOfWorkflowRunID  *snapshot.WorkflowRunID             `json:"retry_of_workflow_run_id,omitempty"`
	CreatedAt             time.Time                           `json:"created_at"`
	StartedAt             *time.Time                          `json:"started_at,omitempty"`
	CompletedAt           *time.Time                          `json:"completed_at,omitempty"`
	// Outstanding elevates work a human still has to resolve: anything still
	// in flight, and any terminal failure. Completed detail collapses.
	Outstanding bool `json:"outstanding"`
	// ErrorMessage is the concise outcome text the run itself recorded.
	ErrorMessage string `json:"error_message,omitempty"`
	// PlannedBuildID is the run's own link identity into build evidence.
	PlannedBuildID *int64 `json:"planned_build_id,omitempty"`
}

// JournalResponse carries the ticket's own identity beside its runs so a
// client that followed a link knows what it is looking at.
type JournalResponse struct {
	TicketID int            `json:"ticket_id"`
	Runs     []JournalEntry `json:"runs"`
}

// ListRuns handles GET /api/v1/agent/tickets/:ticket_id/runs.
//
// Tickets are optional throughout, so a ticket with no runs is an ordinary
// empty journal, not an error.
func (h *Handler) ListRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := ticketIDParam(w, r)
	if !ok {
		return
	}
	if h.runs == nil || h.tickets == nil {
		http.Error(w, "workflow run journal is unavailable", http.StatusInternalServerError)
		return
	}
	if _, found, err := h.tickets.Get(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if !found {
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	}

	ticketID := int64(id)
	runs, err := h.runs.List(r.Context(), db.AgentWorkflowRunListFilter{
		TeamID: h.team.ID, TicketID: &ticketID, Limit: journalPageLimit,
	})
	if err != nil {
		http.Error(w, "failed to read the ticket journal", http.StatusInternalServerError)
		return
	}

	entries := make([]JournalEntry, 0, len(runs))
	for _, run := range runs {
		entry, ok := presentJournalEntry(h.team.ID, id, run)
		if !ok {
			http.Error(w, "invalid durable workflow run", http.StatusInternalServerError)
			return
		}
		entries = append(entries, entry)
	}
	// The store returns newest-first for paging; a journal reads forwards.
	// Concurrent runs order by occurrence time, then by durable identity so
	// the order is total and stable rather than arbitrary.
	sort.SliceStable(entries, func(left, right int) bool {
		if !entries[left].CreatedAt.Equal(entries[right].CreatedAt) {
			return entries[left].CreatedAt.Before(entries[right].CreatedAt)
		}
		return entries[left].WorkflowRunID < entries[right].WorkflowRunID
	})
	writeJSON(w, http.StatusOK, JournalResponse{TicketID: id, Runs: entries})
}

func presentJournalEntry(
	teamID int,
	ticketID int,
	run db.AgentWorkflowRun,
) (JournalEntry, bool) {
	if run.TeamID != teamID || run.ID.Validate() != nil || run.WorkflowName == "" ||
		run.WorkflowVersion <= 0 || run.CreatedAt.IsZero() || run.Status.Validate() != nil {
		return JournalEntry{}, false
	}
	// The filter already scoped this read, but a run that is not this ticket's
	// must never be presented as part of its history.
	if run.TicketID == nil || *run.TicketID != int64(ticketID) {
		return JournalEntry{}, false
	}
	return JournalEntry{
		WorkflowRunID: run.ID, WorkflowName: run.WorkflowName,
		WorkflowVersion: run.WorkflowVersion, DefinitionContentHash: run.DefinitionContentHash,
		Status: run.Status, ExecutionStatus: run.ExecutionStatus,
		OriginKind: run.OriginKind, OriginReference: run.OriginReference,
		RetryOfWorkflowRunID: run.RetryOfWorkflowRunID,
		CreatedAt:            run.CreatedAt, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt,
		Outstanding:  journalEntryOutstanding(run.Status),
		ErrorMessage: run.ErrorMessage, PlannedBuildID: run.PlannedBuildID,
	}, true
}

// journalEntryOutstanding restates the run-level attention rule the workflow
// pages already use, so the journal and the run list cannot contradict each
// other about what still needs a human.
func journalEntryOutstanding(status db.AgentWorkflowRunStatus) bool {
	return slices.Contains(db.ActiveAgentWorkflowRunStatuses, string(status)) ||
		slices.Contains(db.UnresolvedAgentWorkflowRunStatuses, string(status))
}
