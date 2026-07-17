package db

import (
	"database/sql"
	"errors"
	"strconv"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/tickets"
)

//counterfeiter:generate . AgentTicketsFactory
type AgentTicketsFactory interface {
	tickets.Store
}

func NewAgentTicketsFactory(conn DbConn) AgentTicketsFactory {
	return &agentTicketsFactory{conn: conn}
}

type agentTicketsFactory struct {
	conn DbConn
}

func ticketNullInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func ticketNullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// Create inserts a ticket in state 'draft' (queueing is a separate
// Transition call — single-writer discipline) and returns its id, which
// IS the ticket number (branch agent/ticket-<id>, contracts §1.7).
func (f *agentTicketsFactory) Create(t *tickets.Ticket) (int, error) {
	origin := t.Origin
	if origin == "" {
		origin = "web"
	}
	targetBranch := t.TargetBranch
	if targetBranch == "" {
		targetBranch = "main"
	}

	var id int
	err := psql.Insert("agent_tickets").
		Columns(
			"title", "body", "origin", "repo", "target_branch",
			"workflow_name", "workflow_version", "budget_usd",
			"user_id", "user_name", "created_by", "external_ref",
		).
		Values(
			t.Title, t.Body, origin, t.Repo, targetBranch,
			t.WorkflowName, ticketNullInt(t.WorkflowVersion), ticketNullFloat(t.BudgetUSD),
			ticketNullInt(t.UserID), t.UserName, t.CreatedBy, t.ExternalRef,
		).
		Suffix("RETURNING id").
		RunWith(f.conn).
		QueryRow().
		Scan(&id)
	return id, err
}

const ticketColumns = `t.id, t.title, t.body, t.state, t.origin, t.repo, t.target_branch,
	t.workflow_name, t.workflow_version, t.workflow_definition_id,
	t.budget_usd, t.user_id, t.user_name, t.created_by, t.external_ref,
	t.pipeline_run_id, t.branch, t.attempt_count, t.error_detail,
	EXTRACT(EPOCH FROM t.created_at)::bigint,
	EXTRACT(EPOCH FROM t.updated_at)::bigint,
	COALESCE(EXTRACT(EPOCH FROM t.completed_at)::bigint, 0)`

type ticketScanner interface {
	Scan(...any) error
}

func scanTicket(row ticketScanner) (*tickets.Ticket, error) {
	var t tickets.Ticket
	var wfVersion, wfDefID, userID, runID sql.NullInt64
	var budget sql.NullFloat64
	err := row.Scan(
		&t.ID, &t.Title, &t.Body, &t.State, &t.Origin, &t.Repo, &t.TargetBranch,
		&t.WorkflowName, &wfVersion, &wfDefID,
		&budget, &userID, &t.UserName, &t.CreatedBy, &t.ExternalRef,
		&runID, &t.Branch, &t.AttemptCount, &t.ErrorDetail,
		&t.CreatedAt, &t.UpdatedAt, &t.CompletedAt,
	)
	if err != nil {
		return nil, err
	}
	if wfVersion.Valid {
		v := int(wfVersion.Int64)
		t.WorkflowVersion = &v
	}
	if wfDefID.Valid {
		v := int(wfDefID.Int64)
		t.WorkflowDefinitionID = &v
	}
	if userID.Valid {
		v := int(userID.Int64)
		t.UserID = &v
	}
	if runID.Valid {
		v := int(runID.Int64)
		t.PipelineRunID = &v
	}
	if budget.Valid {
		v := budget.Float64
		t.BudgetUSD = &v
	}
	return &t, nil
}

func (f *agentTicketsFactory) Get(id int) (*tickets.Ticket, bool, error) {
	t, err := scanTicket(f.conn.QueryRow(
		`SELECT `+ticketColumns+` FROM agent_tickets t WHERE t.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

func (f *agentTicketsFactory) List(filter tickets.ListFilter) ([]tickets.Ticket, error) {
	query := `SELECT ` + ticketColumns + ` FROM agent_tickets t WHERE true`
	args := []any{}
	if filter.State != "" {
		args = append(args, string(filter.State))
		query += ` AND t.state = $` + strconv.Itoa(len(args))
	}
	if filter.Repo != "" {
		args = append(args, filter.Repo)
		query += ` AND t.repo = $` + strconv.Itoa(len(args))
	}
	if filter.Origin != "" {
		args = append(args, filter.Origin)
		query += ` AND t.origin = $` + strconv.Itoa(len(args))
	}
	query += ` ORDER BY t.created_at DESC, t.id DESC`
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		query += ` LIMIT $` + strconv.Itoa(len(args))
	}

	rows, err := f.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := []tickets.Ticket{}
	for rows.Next() {
		t, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, *t)
	}
	return results, rows.Err()
}

func (f *agentTicketsFactory) Update(id int, upd tickets.Update) error {
	q := psql.Update("agent_tickets").
		Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id})
	if upd.Title != nil {
		q = q.Set("title", *upd.Title)
	}
	if upd.Body != nil {
		q = q.Set("body", *upd.Body)
	}
	if upd.BudgetUSD != nil {
		q = q.Set("budget_usd", *upd.BudgetUSD)
	}
	if upd.WorkflowName != nil {
		q = q.Set("workflow_name", *upd.WorkflowName)
	}
	if upd.WorkflowVersion != nil {
		q = q.Set("workflow_version", *upd.WorkflowVersion)
	}
	if upd.TargetBranch != nil {
		q = q.Set("target_branch", *upd.TargetBranch)
	}

	res, err := q.RunWith(f.conn).Exec()
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tickets.ErrTicketNotFound
	}
	return nil
}

// Transition is THE single writer of agent_tickets.state
// (00-shared-contracts.md §2.1). It validates the edge against the
// §1.7 state machine, then updates guarded by the expected `from`
// state (optimistic concurrency): a concurrent writer that moved the
// ticket first makes this call return ErrStaleTransition instead of
// silently double-applying. Side effects per the ticket-core contract
// addendum.
func (f *agentTicketsFactory) Transition(id int, from, to tickets.State, meta tickets.TransitionMeta) error {
	if !tickets.ValidTransition(from, to) {
		return tickets.ErrInvalidTransition
	}

	q := psql.Update("agent_tickets").
		Set("state", string(to)).
		Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": id, "state": string(from)})

	switch to {
	case tickets.StateDraft: // unqueue
		q = q.Set("queued_at", nil)
	case tickets.StateQueued:
		q = q.Set("queued_at", sq.Expr("now()")).
			Set("completed_at", nil)
		if from == tickets.StateRunning {
			// running → queued (retryable platform error OR rejected
			// send_back checkpoint re-dispatch; attempt_count++) — called
			// by dispatch's retry path and its run-completion reconciler.
			q = q.Set("attempt_count", sq.Expr("attempt_count + 1"))
		}
	case tickets.StateRunning:
		q = q.Set("dispatched_at", sq.Expr("now()"))
		if meta.PipelineRunID != nil {
			q = q.Set("pipeline_run_id", *meta.PipelineRunID)
		}
	case tickets.StateNeedsReview:
		if meta.Branch != "" {
			q = q.Set("branch", meta.Branch)
		}
	case tickets.StateMerged, tickets.StateMergedWithFixes, tickets.StateSentBack,
		tickets.StateAbandoned, tickets.StateConcluded, tickets.StateFailed, tickets.StateErrored:
		q = q.Set("completed_at", sq.Expr("now()"))
		if to == tickets.StateErrored {
			q = q.Set("error_detail", meta.ErrorDetail)
		}
	}

	res, err := q.RunWith(f.conn).Exec()
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		// Distinguish "ticket gone" from "state moved under us".
		_, found, err := f.Get(id)
		if err != nil {
			return err
		}
		if !found {
			return tickets.ErrTicketNotFound
		}
		return tickets.ErrStaleTransition
	}
	return nil
}

// The spec/plan family is implemented in Task 6.
func (f *agentTicketsFactory) SubmitSpec(ticketID int, spec tickets.Spec) (int, error) {
	return 0, errors.New("agentTicketsFactory.SubmitSpec: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) SubmitPlan(ticketID int, ts []tickets.Task) (int, error) {
	return 0, errors.New("agentTicketsFactory.SubmitPlan: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) UpdateTaskStatus(ticketID int, planVersion, ordering int, status tickets.TaskStatus) error {
	return errors.New("agentTicketsFactory.UpdateTaskStatus: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) AppendTaskNote(ticketID int, planVersion, ordering int, note string) error {
	return errors.New("agentTicketsFactory.AppendTaskNote: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) ActivePlan(ticketID int) ([]tickets.Task, error) {
	return nil, errors.New("agentTicketsFactory.ActivePlan: not yet implemented (plan task 6)")
}

func (f *agentTicketsFactory) LatestSpec(ticketID int) (*tickets.Spec, bool, error) {
	return nil, false, errors.New("agentTicketsFactory.LatestSpec: not yet implemented (plan task 6)")
}
