package db

import (
	"database/sql"
	"encoding/json"
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
	if upd.UserID != nil {
		q = q.Set("user_id", *upd.UserID)
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

func emptyIfNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func emptyIfNilLinks(l []tickets.Link) []tickets.Link {
	if l == nil {
		return []tickets.Link{}
	}
	return l
}

// lockTicket takes a FOR UPDATE row lock on the ticket inside tx so
// concurrent spec/plan submissions serialize their version allocation.
func lockTicket(tx Tx, ticketID int) error {
	var one int
	err := tx.QueryRow(`SELECT 1 FROM agent_tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return tickets.ErrTicketNotFound
	}
	return err
}

func (f *agentTicketsFactory) SubmitSpec(ticketID int, spec tickets.Spec) (int, error) {
	criteria, err := json.Marshal(emptyIfNilStrings(spec.AcceptanceCriteria))
	if err != nil {
		return 0, err
	}
	links, err := json.Marshal(emptyIfNilLinks(spec.Links))
	if err != nil {
		return 0, err
	}

	tx, err := f.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	if err := lockTicket(tx, ticketID); err != nil {
		return 0, err
	}

	var version int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(version), 0) + 1 FROM agent_ticket_specs WHERE ticket_id = $1`,
		ticketID).Scan(&version)
	if err != nil {
		return 0, err
	}

	_, err = tx.Exec(
		`INSERT INTO agent_ticket_specs
			(ticket_id, version, title, body, acceptance_criteria, links, submitted_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		ticketID, version, spec.Title, spec.Body, criteria, links, spec.SubmittedBy)
	if err != nil {
		return 0, err
	}

	return version, tx.Commit()
}

func (f *agentTicketsFactory) LatestSpec(ticketID int) (*tickets.Spec, bool, error) {
	var s tickets.Spec
	var criteria, links []byte
	err := f.conn.QueryRow(
		`SELECT id, ticket_id, version, title, body, acceptance_criteria, links, submitted_by,
			EXTRACT(EPOCH FROM created_at)::bigint
		 FROM agent_ticket_specs
		 WHERE ticket_id = $1
		 ORDER BY version DESC
		 LIMIT 1`, ticketID).
		Scan(&s.ID, &s.TicketID, &s.Version, &s.Title, &s.Body, &criteria, &links,
			&s.SubmittedBy, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(criteria, &s.AcceptanceCriteria); err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(links, &s.Links); err != nil {
		return nil, false, err
	}
	return &s, true, nil
}

// SubmitPlan replaces the active plan: new plan_version, orderings 1..N
// as given (contracts §3.2 submit_plan). Old versions are retained for
// process intelligence (§1.7).
func (f *agentTicketsFactory) SubmitPlan(ticketID int, ts []tickets.Task) (int, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	if err := lockTicket(tx, ticketID); err != nil {
		return 0, err
	}

	var planVersion int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(plan_version), 0) + 1 FROM agent_ticket_tasks WHERE ticket_id = $1`,
		ticketID).Scan(&planVersion)
	if err != nil {
		return 0, err
	}

	for i, task := range ts {
		status := task.Status
		if status == "" {
			status = tickets.TaskPending
		}
		_, err = tx.Exec(
			`INSERT INTO agent_ticket_tasks
				(ticket_id, plan_version, ordering, title, detail, status)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			ticketID, planVersion, i+1, task.Title, task.Detail, string(status))
		if err != nil {
			return 0, err
		}
	}

	return planVersion, tx.Commit()
}

func (f *agentTicketsFactory) ActivePlan(ticketID int) ([]tickets.Task, error) {
	rows, err := f.conn.Query(
		`SELECT id, ticket_id, plan_version, ordering, title, detail, status,
			EXTRACT(EPOCH FROM updated_at)::bigint
		 FROM agent_ticket_tasks
		 WHERE ticket_id = $1
		   AND plan_version = (SELECT COALESCE(MAX(plan_version), 0)
		                       FROM agent_ticket_tasks WHERE ticket_id = $1)
		 ORDER BY ordering ASC`, ticketID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []tickets.Task{}
	for rows.Next() {
		var t tickets.Task
		if err := rows.Scan(&t.ID, &t.TicketID, &t.PlanVersion, &t.Ordering,
			&t.Title, &t.Detail, &t.Status, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (f *agentTicketsFactory) UpdateTaskStatus(ticketID int, planVersion, ordering int, status tickets.TaskStatus) error {
	res, err := f.conn.Exec(
		`UPDATE agent_ticket_tasks SET status = $1, updated_at = now()
		 WHERE ticket_id = $2 AND plan_version = $3 AND ordering = $4`,
		string(status), ticketID, planVersion, ordering)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tickets.ErrTaskNotFound
	}
	return nil
}

// UpdateActiveTask atomically applies a status update (plus optional
// note append) to the ACTIVE plan's task. Resolving the active
// plan_version and writing against it happen inside one transaction
// holding the ticket's FOR UPDATE row lock — the same lock SubmitPlan
// takes to allocate versions — so a concurrent plan replacement can
// never slip between the read and the write (the TOCTOU lost-update
// found by agent-review-native #7, 2026-07-17).
func (f *agentTicketsFactory) UpdateActiveTask(ticketID, ordering int, status tickets.TaskStatus, note string) (int, error) {
	tx, err := f.conn.Begin()
	if err != nil {
		return 0, err
	}
	defer Rollback(tx)

	if err := lockTicket(tx, ticketID); err != nil {
		return 0, err
	}

	var planVersion int
	err = tx.QueryRow(
		`SELECT COALESCE(MAX(plan_version), 0) FROM agent_ticket_tasks WHERE ticket_id = $1`,
		ticketID).Scan(&planVersion)
	if err != nil {
		return 0, err
	}
	if planVersion == 0 {
		return 0, tickets.ErrNoActivePlan
	}

	res, err := tx.Exec(
		`UPDATE agent_ticket_tasks
		 SET status = $1,
		     detail = CASE WHEN $2 = '' THEN detail
		                   WHEN detail = '' THEN '> ' || $2
		                   ELSE detail || E'\n\n> ' || $2 END,
		     updated_at = now()
		 WHERE ticket_id = $3 AND plan_version = $4 AND ordering = $5`,
		string(status), note, ticketID, planVersion, ordering)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, tickets.ErrTaskNotFound
	}

	return planVersion, tx.Commit()
}

// AppendTaskNote appends the §3.2 update_task_status note as a markdown
// blockquote on the task's detail (ticket-core contract addendum).
func (f *agentTicketsFactory) AppendTaskNote(ticketID int, planVersion, ordering int, note string) error {
	res, err := f.conn.Exec(
		`UPDATE agent_ticket_tasks
		 SET detail = CASE WHEN detail = '' THEN '> ' || $1
		                   ELSE detail || E'\n\n> ' || $1 END,
		     updated_at = now()
		 WHERE ticket_id = $2 AND plan_version = $3 AND ordering = $4`,
		note, ticketID, planVersion, ordering)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tickets.ErrTaskNotFound
	}
	return nil
}
