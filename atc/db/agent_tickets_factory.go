package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/agent/api/tickets"
	"github.com/concourse/concourse/agent/snapshot"
	"github.com/concourse/concourse/agent/workitem"
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

func ticketNullSnapshotID(p *snapshot.SnapshotID) any {
	if p == nil {
		return nil
	}
	return int64(*p)
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
			"user_id", "user_name", "created_by", "external_ref", "repository_snapshot_id",
		).
		Values(
			t.Title, t.Body, origin, t.Repo, targetBranch,
			t.WorkflowName, ticketNullInt(t.WorkflowVersion), ticketNullFloat(t.BudgetUSD),
			ticketNullInt(t.UserID), t.UserName, t.CreatedBy, t.ExternalRef, ticketNullSnapshotID(t.RepositorySnapshotID),
		).
		Suffix("RETURNING id").
		RunWith(f.conn).
		QueryRow().
		Scan(&id)
	return id, err
}

const ticketColumns = `t.id, t.revision, t.title, t.body, t.state, t.origin, t.repo, t.target_branch,
	t.workflow_name, t.workflow_version, t.workflow_definition_id,
	t.budget_usd, t.user_id, t.user_name, t.created_by, t.external_ref,
	t.pipeline_run_id, t.workflow_run_id, t.work_item_snapshot_id, t.repository_snapshot_id,
	t.dispatch_reservation_key, t.branch, t.attempt_count, t.error_detail,
	EXTRACT(EPOCH FROM t.created_at)::bigint,
	EXTRACT(EPOCH FROM t.updated_at)::bigint,
	COALESCE(EXTRACT(EPOCH FROM t.completed_at)::bigint, 0)`

type ticketScanner interface {
	Scan(...any) error
}

func scanTicket(row ticketScanner) (*tickets.Ticket, error) {
	var t tickets.Ticket
	var wfVersion, wfDefID, userID, runID, workflowRunID, workItemSnapshotID, repositorySnapshotID sql.NullInt64
	var budget sql.NullFloat64
	err := row.Scan(
		&t.ID, &t.Revision, &t.Title, &t.Body, &t.State, &t.Origin, &t.Repo, &t.TargetBranch,
		&t.WorkflowName, &wfVersion, &wfDefID,
		&budget, &userID, &t.UserName, &t.CreatedBy, &t.ExternalRef,
		&runID, &workflowRunID, &workItemSnapshotID, &repositorySnapshotID,
		&t.DispatchReservationKey, &t.Branch, &t.AttemptCount, &t.ErrorDetail,
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
	if workflowRunID.Valid {
		v := snapshot.WorkflowRunID(workflowRunID.Int64)
		t.WorkflowRunID = &v
	}
	if workItemSnapshotID.Valid {
		v := snapshot.SnapshotID(workItemSnapshotID.Int64)
		t.WorkItemSnapshotID = &v
	}
	if repositorySnapshotID.Valid {
		v := snapshot.SnapshotID(repositorySnapshotID.Int64)
		t.RepositorySnapshotID = &v
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
	protected := false
	q := psql.Update("agent_tickets").
		Set("updated_at", sq.Expr("now()")).
		Set("revision", sq.Expr("revision + 1")).
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
		protected = true
		q = q.Where(sq.Or{sq.Eq{"dispatch_reservation_key": ""}, sq.Eq{"workflow_name": *upd.WorkflowName}})
		q = q.Set("workflow_name", *upd.WorkflowName)
	}
	if upd.WorkflowVersion != nil {
		protected = true
		q = q.Where(sq.Or{sq.Eq{"dispatch_reservation_key": ""}, sq.Eq{"workflow_version": *upd.WorkflowVersion}})
		q = q.Set("workflow_version", *upd.WorkflowVersion)
	}
	if upd.UserID != nil {
		q = q.Set("user_id", *upd.UserID)
	}
	if upd.TargetBranch != nil {
		q = q.Set("target_branch", *upd.TargetBranch)
	}
	if upd.RepositorySnapshotID != nil {
		if err := upd.RepositorySnapshotID.Validate(); err != nil {
			return tickets.ErrDispatchConflict
		}
		protected = true
		q = q.Where(sq.Or{
			sq.Eq{"dispatch_reservation_key": ""},
			sq.Expr("repository_snapshot_id IS NULL"),
			sq.Eq{"repository_snapshot_id": int64(*upd.RepositorySnapshotID)},
		})
		q = q.Set("repository_snapshot_id", int64(*upd.RepositorySnapshotID))
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
		if protected {
			_, found, readErr := f.Get(id)
			if readErr != nil {
				return readErr
			}
			if found {
				return tickets.ErrDispatchConflict
			}
		}
		return tickets.ErrTicketNotFound
	}
	return nil
}

func (f *agentTicketsFactory) ReserveDispatch(
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
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return tickets.DispatchReservation{}, err
	}
	defer Rollback(tx)

	ticket, err := scanTicket(tx.QueryRowContext(ctx,
		`SELECT `+ticketColumns+` FROM agent_tickets t WHERE t.id = $1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return tickets.DispatchReservation{}, tickets.ErrTicketNotFound
	}
	if err != nil {
		return tickets.DispatchReservation{}, err
	}
	if ticket.DispatchReservationKey != "" {
		if ticket.WorkflowVersion == nil || *ticket.WorkflowVersion != request.WorkflowVersion ||
			ticket.WorkflowDefinitionID == nil || *ticket.WorkflowDefinitionID != request.WorkflowDefinitionID ||
			(ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
			return tickets.DispatchReservation{}, tickets.ErrDispatchConflict
		}
		if err := tx.Commit(); err != nil {
			return tickets.DispatchReservation{}, err
		}
		return tickets.DispatchReservation{Key: ticket.DispatchReservationKey, Ticket: *ticket}, nil
	}
	if ticket.State != tickets.StateQueued {
		return tickets.DispatchReservation{}, tickets.ErrStaleTransition
	}
	if ticket.Revision != request.ExpectedRevision {
		return tickets.DispatchReservation{}, tickets.ErrDispatchConflict
	}
	key := fmt.Sprintf("ticket-dispatch/v1/ticket/%d/attempt/%d/revision/%d", ticket.ID, ticket.AttemptCount, ticket.Revision)
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_tickets
		SET workflow_version = $2,
		    workflow_definition_id = $3,
		    dispatch_reservation_key = $4,
		    revision = revision + 1,
		    updated_at = now()
		WHERE id = $1
	`, id, request.WorkflowVersion, request.WorkflowDefinitionID, key)
	if err != nil {
		return tickets.DispatchReservation{}, err
	}
	ticket, err = scanTicket(tx.QueryRowContext(ctx,
		`SELECT `+ticketColumns+` FROM agent_tickets t WHERE t.id = $1`, id))
	if err != nil {
		return tickets.DispatchReservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return tickets.DispatchReservation{}, err
	}
	return tickets.DispatchReservation{Key: key, Ticket: *ticket, Created: true}, nil
}

func (f *agentTicketsFactory) RecordDispatchWorkItem(
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
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	ticket, err := scanTicket(tx.QueryRowContext(ctx,
		`SELECT `+ticketColumns+` FROM agent_tickets t WHERE t.id = $1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return tickets.ErrTicketNotFound
	}
	if err != nil {
		return err
	}
	if ticket.DispatchReservationKey != reservationKey ||
		(ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
		return tickets.ErrDispatchConflict
	}
	if ticket.WorkItemSnapshotID != nil {
		if *ticket.WorkItemSnapshotID != snapshotID {
			return tickets.ErrDispatchConflict
		}
		return tx.Commit()
	}
	if ticket.Revision != expectedRevision {
		return tickets.ErrDispatchConflict
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_tickets
		SET work_item_snapshot_id = $2, revision = revision + 1, updated_at = now()
		WHERE id = $1
	`, id, int64(snapshotID))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (f *agentTicketsFactory) RecordDispatchRun(
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
	tx, err := f.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer Rollback(tx)
	ticket, err := scanTicket(tx.QueryRowContext(ctx,
		`SELECT `+ticketColumns+` FROM agent_tickets t WHERE t.id = $1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return tickets.ErrTicketNotFound
	}
	if err != nil {
		return err
	}
	if ticket.DispatchReservationKey != reservationKey || ticket.WorkItemSnapshotID == nil ||
		ticket.RepositorySnapshotID == nil || (ticket.State != tickets.StateQueued && ticket.State != tickets.StateRunning) {
		return tickets.ErrDispatchConflict
	}
	if ticket.WorkflowRunID != nil || ticket.PipelineRunID != nil {
		if ticket.WorkflowRunID == nil || ticket.PipelineRunID == nil ||
			*ticket.WorkflowRunID != workflowRunID || *ticket.PipelineRunID != pipelineRunID {
			return tickets.ErrDispatchConflict
		}
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_tickets
		SET workflow_run_id = $2, pipeline_run_id = $3,
		    revision = revision + 1, updated_at = now()
		WHERE id = $1
	`, id, int64(workflowRunID), pipelineRunID)
	if err != nil {
		return err
	}
	return tx.Commit()
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
		Set("revision", sq.Expr("revision + 1")).
		Where(sq.Eq{"id": id, "state": string(from)})

	switch to {
	case tickets.StateDraft: // unqueue
		q = q.Set("queued_at", nil).
			Set("dispatch_reservation_key", "").
			Set("workflow_run_id", nil).
			Set("work_item_snapshot_id", nil).
			Set("pipeline_run_id", nil)
	case tickets.StateQueued:
		q = q.Set("queued_at", sq.Expr("now()")).
			Set("completed_at", nil)
		if from == tickets.StateRunning {
			// running → queued is the generic explicit/manual retry edge;
			// each retry increments attempt_count.
			q = q.Set("attempt_count", sq.Expr("attempt_count + 1"))
		}
		if from != tickets.StateDraft {
			q = q.Set("dispatch_reservation_key", "").
				Set("workflow_run_id", nil).
				Set("work_item_snapshot_id", nil).
				Set("pipeline_run_id", nil)
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

func bumpTicketRevision(tx Tx, ticketID int) error {
	result, err := tx.Exec(`
		UPDATE agent_tickets
		SET revision = revision + 1, updated_at = now()
		WHERE id = $1
	`, ticketID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return tickets.ErrTicketNotFound
	}
	return nil
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

const captureTicketRevisionSQL = `
	SELECT t.id,
	       t.revision,
	       t.updated_at,
	       t.origin,
	       t.external_ref,
	       t.title,
	       t.body,
	       t.state,
	       t.workflow_name,
	       t.workflow_version,
	       t.workflow_definition_id,
	       COALESCE(latest_spec.payload, 'null'::jsonb),
	       COALESCE(active_plan.payload, 'null'::jsonb)
	FROM agent_tickets t
	LEFT JOIN LATERAL (
		SELECT jsonb_build_object(
			'version', s.version,
			'title', s.title,
			'body', s.body,
			'acceptance_criteria', s.acceptance_criteria,
			'links', s.links,
			'submitted_by', s.submitted_by,
			'created_at', EXTRACT(EPOCH FROM s.created_at)::bigint
		) AS payload
		FROM agent_ticket_specs s
		WHERE s.ticket_id = t.id
		ORDER BY s.version DESC
		LIMIT 1
	) latest_spec ON TRUE
	LEFT JOIN LATERAL (
		SELECT jsonb_build_object(
			'version', task.plan_version,
			'tasks', jsonb_agg(
				jsonb_build_object(
					'ordering', task.ordering,
					'title', task.title,
					'detail', task.detail,
					'status', task.status,
					'updated_at', EXTRACT(EPOCH FROM task.updated_at)::bigint
				) ORDER BY task.ordering
			)
		) AS payload
		FROM agent_ticket_tasks task
		WHERE task.ticket_id = t.id
		  AND task.plan_version = (
			SELECT MAX(candidate.plan_version)
			FROM agent_ticket_tasks candidate
			WHERE candidate.ticket_id = t.id
		  )
		GROUP BY task.plan_version
	) active_plan ON TRUE
	WHERE t.id = $1
`

func (f *agentTicketsFactory) CaptureRevision(ctx context.Context, ticketID int) (workitem.CapturedRevision, bool, error) {
	if ctx == nil {
		return workitem.CapturedRevision{}, false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	tx, err := f.conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	defer Rollback(tx)

	var (
		revision    workitem.Revision
		origin      string
		externalRef string
		workflowVer sql.NullInt64
		workflowDef sql.NullInt64
		specJSON    []byte
		planJSON    []byte
	)
	err = tx.QueryRowContext(ctx, captureTicketRevisionSQL, ticketID).Scan(
		&revision.TicketID,
		&revision.Revision,
		&revision.UpdatedAt,
		&origin,
		&externalRef,
		&revision.Title,
		&revision.Body,
		&revision.State,
		&revision.Workflow.Name,
		&workflowVer,
		&workflowDef,
		&specJSON,
		&planJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workitem.CapturedRevision{}, false, nil
	}
	if err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	if origin == "jira" {
		revision.Adapter = "jira"
	} else {
		revision.Adapter = "jetbridge"
	}
	if externalRef != "" {
		revision.ExternalID = externalRef
	} else {
		revision.ExternalID = strconv.Itoa(revision.TicketID)
	}
	if workflowVer.Valid {
		value := int(workflowVer.Int64)
		revision.Workflow.Version = &value
	}
	if workflowDef.Valid {
		value := int(workflowDef.Int64)
		revision.Workflow.DefinitionID = &value
	}
	if err := json.Unmarshal(specJSON, &revision.Spec); err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	if err := json.Unmarshal(planJSON, &revision.Plan); err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	captured, err := workitem.MarshalRevision(revision)
	if err != nil {
		return workitem.CapturedRevision{}, true, err
	}
	if err := tx.Commit(); err != nil {
		return workitem.CapturedRevision{}, true, err
	}
	return captured, true, nil
}
