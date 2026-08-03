package db

import (
	"context"
	"database/sql"
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
			"workflow_name", "workflow_version",
			"user_id", "user_name", "created_by", "external_ref", "repository_snapshot_id",
		).
		Values(
			t.Title, t.Body, origin, t.Repo, targetBranch,
			t.WorkflowName, ticketNullInt(t.WorkflowVersion),
			ticketNullInt(t.UserID), t.UserName, t.CreatedBy, t.ExternalRef, ticketNullSnapshotID(t.RepositorySnapshotID),
		).
		Suffix("RETURNING id").
		RunWith(f.conn).
		QueryRow().
		Scan(&id)
	return id, err
}

// ticketCurrentWorkflowRun derives the run of the ticket's CURRENT dispatch
// attempt, replacing the stored agent_tickets.workflow_run_id that migration
// 1773106158 dropped. A ticket now drives many runs across many workflows —
// that whole set lives on agent_workflow_runs.ticket_id — so the one fact this
// column still owes its callers is which run the live reservation admitted.
//
// DispatchOne passes the reservation key straight through as the run's
// idempotency key, precisely so a re-entry recovers the same run; that makes
// the reservation key an exact, unique handle on the current attempt. Clearing
// the reservation on unqueue or requeue therefore clears this read for free,
// which is exactly what the old column's explicit nulling did. Indexed by
// agent_workflow_runs_idempotency_key.
const ticketCurrentWorkflowRun = `(
		SELECT r.id FROM agent_workflow_runs r
		WHERE t.dispatch_reservation_key <> ''
		  AND r.idempotency_key = t.dispatch_reservation_key
		ORDER BY r.id DESC
		LIMIT 1
	)`

const ticketColumns = `t.id, t.revision, t.title, t.body, t.state, t.origin, t.repo, t.target_branch,
	t.workflow_name, t.workflow_version, t.workflow_definition_id,
	t.user_id, t.user_name, t.created_by, t.external_ref,
	t.pipeline_run_id, ` + ticketCurrentWorkflowRun + `, t.work_item_snapshot_id, t.repository_snapshot_id,
	t.dispatch_reservation_key, t.attempt_count,
	EXTRACT(EPOCH FROM t.created_at)::bigint,
	EXTRACT(EPOCH FROM t.updated_at)::bigint,
	COALESCE(EXTRACT(EPOCH FROM t.completed_at)::bigint, 0)`

type ticketScanner interface {
	Scan(...any) error
}

func scanTicket(row ticketScanner) (*tickets.Ticket, error) {
	var t tickets.Ticket
	var wfVersion, wfDefID, userID, runID, workflowRunID, workItemSnapshotID, repositorySnapshotID sql.NullInt64
	err := row.Scan(
		&t.ID, &t.Revision, &t.Title, &t.Body, &t.State, &t.Origin, &t.Repo, &t.TargetBranch,
		&t.WorkflowName, &wfVersion, &wfDefID,
		&userID, &t.UserName, &t.CreatedBy, &t.ExternalRef,
		&runID, &workflowRunID, &workItemSnapshotID, &repositorySnapshotID,
		&t.DispatchReservationKey, &t.AttemptCount,
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
	if upd.WorkflowName != nil {
		protected = true
		q = q.Where(sq.Or{sq.Eq{"dispatch_reservation_key": ""}, sq.Eq{"workflow_name": *upd.WorkflowName}})
		q = q.Set("workflow_name", *upd.WorkflowName)
	}
	if upd.WorkflowVersion.Present() {
		protected = true
		value := upd.WorkflowVersion.Value()
		if value == nil {
			q = q.Where(sq.Or{sq.Eq{"dispatch_reservation_key": ""}, sq.Expr("workflow_version IS NULL")})
			q = q.Set("workflow_version", nil)
		} else {
			q = q.Where(sq.Or{sq.Eq{"dispatch_reservation_key": ""}, sq.Eq{"workflow_version": *value}})
			q = q.Set("workflow_version", *value)
		}
	}
	if upd.UserID != nil {
		q = q.Set("user_id", *upd.UserID)
	}
	if upd.TargetBranch != nil {
		q = q.Set("target_branch", *upd.TargetBranch)
	}
	if upd.RepositorySnapshotID.Present() {
		protected = true
		value := upd.RepositorySnapshotID.Value()
		if value == nil {
			q = q.Where(sq.Or{
				sq.Eq{"dispatch_reservation_key": ""},
				sq.Expr("repository_snapshot_id IS NULL"),
			})
			q = q.Set("repository_snapshot_id", nil)
		} else {
			if err := value.Validate(); err != nil {
				return tickets.ErrDispatchConflict
			}
			q = q.Where(sq.Or{
				sq.Eq{"dispatch_reservation_key": ""},
				sq.Expr("repository_snapshot_id IS NULL"),
				sq.Eq{"repository_snapshot_id": int64(*value)},
			})
			q = q.Set("repository_snapshot_id", int64(*value))
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
	// The run identity is no longer written here: the binder wrote it onto the
	// run itself at admission, and this ticket reads it back through its
	// reservation. What is left to record is the execution linkage.
	if ticket.WorkflowRunID == nil || *ticket.WorkflowRunID != workflowRunID {
		return tickets.ErrDispatchConflict
	}
	if ticket.PipelineRunID != nil {
		if *ticket.PipelineRunID != pipelineRunID {
			return tickets.ErrDispatchConflict
		}
		return tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_tickets
		SET pipeline_run_id = $2,
		    revision = revision + 1, updated_at = now()
		WHERE id = $1
	`, id, pipelineRunID)
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
		// Releasing the reservation releases the derived current-run read with
		// it; the run itself keeps its durable association, which is the point.
		q = q.Set("queued_at", nil).
			Set("dispatch_reservation_key", "").
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
				Set("work_item_snapshot_id", nil).
				Set("pipeline_run_id", nil)
		}
	case tickets.StateRunning:
		q = q.Set("dispatched_at", sq.Expr("now()"))
		if meta.PipelineRunID != nil {
			q = q.Set("pipeline_run_id", *meta.PipelineRunID)
		}
	case tickets.StateClosed:
		q = q.Set("completed_at", sq.Expr("now()"))
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

// TransitionCurrentRunToNeedsReview is the run-completion reconciler's
// atomic projection edge. The workflow-run identity, its durable ticket
// association, the live reservation, and the running state are checked in the
// same UPDATE that changes state. A stale run therefore cannot terminalize a
// newer dispatch even when ownership changes immediately before this query
// acquires the ticket row lock.
func (f *agentTicketsFactory) TransitionCurrentRunToNeedsReview(
	ctx context.Context,
	id int,
	workflowRunID snapshot.WorkflowRunID,
) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := f.conn.ExecContext(ctx, `
		UPDATE agent_tickets AS ticket
		SET state = 'needs_review',
		    updated_at = now(),
		    revision = ticket.revision + 1
		FROM agent_workflow_runs AS run
		WHERE ticket.id = $1
		  AND ticket.state = 'running'
		  AND ticket.dispatch_reservation_key <> ''
		  AND run.id = $2
		  AND run.ticket_id = ticket.id
		  AND run.idempotency_key = ticket.dispatch_reservation_key
	`, id, int64(workflowRunID))
	return err
}

// captureTicketRevisionSQL reads exactly what work-item/v1 freezes: the
// authored content at one revision. The ticket's lifecycle state and the
// workflow selected to consume it are deliberately NOT read — they belong to
// the durable run, and capturing them would mint a second copy that is stale
// the moment the ticket moves.
const captureTicketRevisionSQL = `
	SELECT t.id,
	       t.revision,
	       t.updated_at,
	       t.external_ref,
	       t.title,
	       t.body
	FROM agent_tickets t
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
		externalRef string
	)
	err = tx.QueryRowContext(ctx, captureTicketRevisionSQL, ticketID).Scan(
		&revision.TicketID,
		&revision.Revision,
		&revision.UpdatedAt,
		&externalRef,
		&revision.Title,
		&revision.Body,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return workitem.CapturedRevision{}, false, nil
	}
	if err != nil {
		return workitem.CapturedRevision{}, false, err
	}
	revision.Adapter = "jetbridge"
	if externalRef != "" {
		revision.ExternalID = externalRef
	} else {
		revision.ExternalID = strconv.Itoa(revision.TicketID)
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
