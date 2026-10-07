package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/concourse/concourse/atc"
)

// ErrLandingEntryExists means the entry id is already queued or settled for
// another commit. The same commit submitted again is a no-op, not this.
var ErrLandingEntryExists = errors.New("landing entry already exists for another commit")

// LandingQueue is one row of landing_queues.
type LandingQueue struct {
	ID       int
	TeamID   int
	TeamName string
	Name     string
	Config   atc.LandingQueueConfig
}

// LandingEntryRow is one row of landing_entries.
type LandingEntryRow struct {
	ID           int
	QueueID      int
	EntryID      string
	Commit       string
	State        atc.LandingEntryState
	SubmittedBy  string
	SubmittedAt  time.Time
	SettledAt    *time.Time
	SettleReason string
	ComposeRunID *int
	LandRunID    *int
}

// LandingIntent is one row of landing_intents: a candidate from its compose
// Run's admission to its landing or discard.
type LandingIntent struct {
	ID           int
	QueueID      int
	EntryRowID   int
	ComposeRunID int
	LandRunID    *int
	State        string
	Fails        int
	LastError    string
}

const (
	LandingIntentComposing = "composing"
	LandingIntentLanding   = "landing"
	LandingIntentDone      = "done"
)

// LandingQueueFactory is the landing queue's own tables (ADR-0009). The
// component owns every transaction that follows a Run outcome; the settle
// methods take that transaction and are no-ops when the Run they name was
// settled before, so a pass that repeats after a restart changes nothing.
type LandingQueueFactory interface {
	SetQueue(context.Context, int, string, atc.LandingQueueConfig) error
	Queue(context.Context, int, string) (LandingQueue, bool, error)
	Queues(context.Context) ([]LandingQueue, error)
	Submit(context.Context, int, atc.LandingSubmission, string) (bool, error)
	Status(context.Context, LandingQueue) (atc.LandingQueueStatus, error)

	NextQueued(context.Context, Tx, int) (LandingEntryRow, bool, error)
	OpenIntents(context.Context, Tx, int) ([]LandingIntent, error)
	RecordCompose(context.Context, Tx, LandingEntryRow, int) error
	RecordLand(context.Context, Tx, LandingIntent, int) error
	LandEntry(context.Context, Tx, LandingIntent, string) (bool, error)
	EjectEntry(context.Context, Tx, LandingIntent, string) (bool, error)
	Recompose(context.Context, Tx, LandingIntent, string) (bool, error)
	FailLand(context.Context, Tx, LandingIntent, string) (bool, error)
}

type landingQueueFactory struct {
	conn DbConn
}

func NewLandingQueueFactory(conn DbConn) LandingQueueFactory {
	return &landingQueueFactory{conn: conn}
}

func (f *landingQueueFactory) SetQueue(ctx context.Context, teamID int, name string, config atc.LandingQueueConfig) error {
	if err := config.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(config)
	if err != nil {
		return err
	}
	_, err = psql.Insert("landing_queues").
		Columns("team_id", "name", "config").
		Values(teamID, name, body).
		Suffix("ON CONFLICT (team_id, name) DO UPDATE SET config = EXCLUDED.config, updated_at = now()").
		RunWith(f.conn).ExecContext(ctx)
	return err
}

var landingQueuesQuery = psql.Select("q.id", "q.team_id", "t.name", "q.name", "q.config").From("landing_queues q").Join("teams t ON t.id = q.team_id")

func scanLandingQueue(row scannable) (LandingQueue, error) {
	var queue LandingQueue
	var body []byte
	if err := row.Scan(&queue.ID, &queue.TeamID, &queue.TeamName, &queue.Name, &body); err != nil {
		return LandingQueue{}, err
	}
	return queue, json.Unmarshal(body, &queue.Config)
}

func (f *landingQueueFactory) Queue(ctx context.Context, teamID int, name string) (LandingQueue, bool, error) {
	queue, err := scanLandingQueue(landingQueuesQuery.Where(sq.Eq{"q.team_id": teamID, "q.name": name}).RunWith(f.conn).QueryRowContext(ctx))
	if err == sql.ErrNoRows {
		return LandingQueue{}, false, nil
	}
	return queue, err == nil, err
}

func (f *landingQueueFactory) Queues(ctx context.Context) ([]LandingQueue, error) {
	rows, err := landingQueuesQuery.OrderBy("q.id").RunWith(f.conn).QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var queues []LandingQueue
	for rows.Next() {
		queue, err := scanLandingQueue(rows)
		if err != nil {
			return nil, err
		}
		queues = append(queues, queue)
	}
	return queues, rows.Err()
}

// Submit queues an entry. The unique index on (queue_id, entry_id) is the
// dedup: the same id with the same commit changes nothing and reports false;
// with another commit it is ErrLandingEntryExists, whether the first is
// queued or long settled.
func (f *landingQueueFactory) Submit(ctx context.Context, queueID int, sub atc.LandingSubmission, by string) (bool, error) {
	if !atc.ValidLandingEntryID(sub.ID) {
		return false, fmt.Errorf("landing entry id %q is not a safe ref component", sub.ID)
	}
	if !atc.ValidCommitSHA(sub.Commit) {
		return false, fmt.Errorf("landing entry commit %q is not a full sha", sub.Commit)
	}
	result, err := psql.Insert("landing_entries").
		Columns("queue_id", "entry_id", "commit", "submitted_by").
		Values(queueID, sub.ID, sub.Commit, by).
		Suffix("ON CONFLICT (queue_id, entry_id) DO NOTHING").
		RunWith(f.conn).ExecContext(ctx)
	if err != nil {
		return false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if inserted == 1 {
		return true, nil
	}
	var existing string
	err = psql.Select("commit").From("landing_entries").Where(sq.Eq{"queue_id": queueID, "entry_id": sub.ID}).
		RunWith(f.conn).QueryRowContext(ctx).Scan(&existing)
	if err != nil {
		return false, err
	}
	if existing != sub.Commit {
		return false, ErrLandingEntryExists
	}
	return false, nil
}

var landingEntriesQuery = psql.Select("id", "queue_id", "entry_id", "commit", "state", "submitted_by", "submitted_at", "settled_at", "settle_reason", "compose_run_id", "land_run_id").From("landing_entries")

func scanLandingEntry(row scannable) (LandingEntryRow, error) {
	var entry LandingEntryRow
	var settledAt sql.NullTime
	var compose, land sql.NullInt64
	err := row.Scan(&entry.ID, &entry.QueueID, &entry.EntryID, &entry.Commit, &entry.State, &entry.SubmittedBy, &entry.SubmittedAt, &settledAt, &entry.SettleReason, &compose, &land)
	if err != nil {
		return LandingEntryRow{}, err
	}
	if settledAt.Valid {
		t := settledAt.Time
		entry.SettledAt = &t
	}
	entry.ComposeRunID = nullableInt(compose)
	entry.LandRunID = nullableInt(land)
	return entry, nil
}

func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	i := int(v.Int64)
	return &i
}

// NextQueued is the oldest queued entry of the queue, locked for the
// transaction so two passes cannot compose it twice.
func (f *landingQueueFactory) NextQueued(ctx context.Context, tx Tx, queueID int) (LandingEntryRow, bool, error) {
	entry, err := scanLandingEntry(landingEntriesQuery.
		Where(sq.Eq{"queue_id": queueID, "state": atc.LandingEntryQueued}).
		OrderBy("submitted_at", "id").Limit(1).Suffix("FOR UPDATE SKIP LOCKED").
		RunWith(tx).QueryRowContext(ctx))
	if err == sql.ErrNoRows {
		return LandingEntryRow{}, false, nil
	}
	return entry, err == nil, err
}

var landingIntentsQuery = psql.Select("id", "queue_id", "entry_id", "compose_run_id", "land_run_id", "state", "fails", "last_error").From("landing_intents")

func scanLandingIntent(row scannable) (LandingIntent, error) {
	var intent LandingIntent
	var land sql.NullInt64
	err := row.Scan(&intent.ID, &intent.QueueID, &intent.EntryRowID, &intent.ComposeRunID, &land, &intent.State, &intent.Fails, &intent.LastError)
	if err != nil {
		return LandingIntent{}, err
	}
	intent.LandRunID = nullableInt(land)
	return intent, nil
}

// OpenIntents is every intent of the queue that is not done, locked for the
// transaction, oldest first.
func (f *landingQueueFactory) OpenIntents(ctx context.Context, tx Tx, queueID int) ([]LandingIntent, error) {
	rows, err := landingIntentsQuery.Where(sq.And{sq.Eq{"queue_id": queueID}, sq.NotEq{"state": LandingIntentDone}}).
		OrderBy("id").Suffix("FOR UPDATE").RunWith(tx).QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer Close(rows)
	var intents []LandingIntent
	for rows.Next() {
		intent, err := scanLandingIntent(rows)
		if err != nil {
			return nil, err
		}
		intents = append(intents, intent)
	}
	return intents, rows.Err()
}

// RecordCompose creates the candidate's intent at the compose Run's
// admission and takes the entry in flight. compose_run_id is unique, so a
// Run can be recorded once.
func (f *landingQueueFactory) RecordCompose(ctx context.Context, tx Tx, entry LandingEntryRow, composeRunID int) error {
	_, err := psql.Insert("landing_intents").
		Columns("queue_id", "entry_id", "compose_run_id").
		Values(entry.QueueID, entry.ID, composeRunID).
		RunWith(tx).ExecContext(ctx)
	if err != nil {
		return err
	}
	_, err = psql.Update("landing_entries").
		Set("state", atc.LandingEntryInFlight).Set("compose_run_id", composeRunID).Set("land_run_id", nil).
		Where(sq.Eq{"id": entry.ID}).RunWith(tx).ExecContext(ctx)
	return err
}

// RecordLand names the land Run admitted for the candidate.
func (f *landingQueueFactory) RecordLand(ctx context.Context, tx Tx, intent LandingIntent, landRunID int) error {
	_, err := psql.Update("landing_intents").
		Set("state", LandingIntentLanding).Set("land_run_id", landRunID).Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": intent.ID}).RunWith(tx).ExecContext(ctx)
	if err != nil {
		return err
	}
	_, err = psql.Update("landing_entries").Set("land_run_id", landRunID).Where(sq.Eq{"id": intent.EntryRowID}).RunWith(tx).ExecContext(ctx)
	return err
}

// settle closes an intent still in the state it was read in. False means
// another pass settled it first: the caller changes nothing else.
func (f *landingQueueFactory) settle(ctx context.Context, tx Tx, intent LandingIntent, entryState atc.LandingEntryState, reason string, keepRuns bool) (bool, error) {
	result, err := psql.Update("landing_intents").
		Set("state", LandingIntentDone).Set("updated_at", sq.Expr("now()")).
		Where(sq.And{sq.Eq{"id": intent.ID}, sq.NotEq{"state": LandingIntentDone}}).
		RunWith(tx).ExecContext(ctx)
	if err != nil {
		return false, err
	}
	if n, err := result.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	update := psql.Update("landing_entries").Set("state", entryState).Set("settle_reason", reason)
	if entryState == atc.LandingEntryQueued {
		update = update.Set("settled_at", nil)
	} else {
		update = update.Set("settled_at", sq.Expr("now()"))
	}
	if !keepRuns {
		update = update.Set("land_run_id", nil)
	}
	_, err = update.Where(sq.Eq{"id": intent.EntryRowID}).RunWith(tx).ExecContext(ctx)
	return true, err
}

// LandEntry settles the candidate as landed.
func (f *landingQueueFactory) LandEntry(ctx context.Context, tx Tx, intent LandingIntent, reason string) (bool, error) {
	return f.settle(ctx, tx, intent, atc.LandingEntryLanded, reason, true)
}

// EjectEntry settles the entry as ejected, naming why.
func (f *landingQueueFactory) EjectEntry(ctx context.Context, tx Tx, intent LandingIntent, reason string) (bool, error) {
	return f.settle(ctx, tx, intent, atc.LandingEntryEjected, reason, true)
}

// Recompose discards the candidate: the intent closes and the entry is
// queued again, so the next pass composes it on the trunk as it is now.
func (f *landingQueueFactory) Recompose(ctx context.Context, tx Tx, intent LandingIntent, reason string) (bool, error) {
	return f.settle(ctx, tx, intent, atc.LandingEntryQueued, reason, false)
}

// FailLand records a land Run that did not land and frees the intent for
// another land Run of the same candidate. False means the intent no longer
// names that land Run: another pass already moved it.
func (f *landingQueueFactory) FailLand(ctx context.Context, tx Tx, intent LandingIntent, lastError string) (bool, error) {
	if intent.LandRunID == nil {
		return false, nil
	}
	result, err := psql.Update("landing_intents").
		Set("fails", sq.Expr("fails + 1")).Set("last_error", lastError).Set("land_run_id", nil).Set("updated_at", sq.Expr("now()")).
		Where(sq.Eq{"id": intent.ID, "land_run_id": *intent.LandRunID}).
		RunWith(tx).ExecContext(ctx)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

// Status is the queue as fly prints it: every entry with its Runs' numbers,
// and the open intent's count of land Runs that did not land.
func (f *landingQueueFactory) Status(ctx context.Context, queue LandingQueue) (atc.LandingQueueStatus, error) {
	status := atc.LandingQueueStatus{Name: queue.Name, Config: queue.Config, Entries: []atc.LandingEntry{}}
	rows, err := psql.Select("e.entry_id", "e.commit", "e.state", "e.submitted_by", "e.submitted_at", "e.settled_at", "e.settle_reason",
		"COALESCE(c.number, 0)", "COALESCE(l.number, 0)").
		From("landing_entries e").
		LeftJoin("pipeline_runs c ON c.id = e.compose_run_id").
		LeftJoin("pipeline_runs l ON l.id = e.land_run_id").
		Where(sq.Eq{"e.queue_id": queue.ID}).OrderBy("e.submitted_at", "e.id").
		RunWith(f.conn).QueryContext(ctx)
	if err != nil {
		return status, err
	}
	defer Close(rows)
	for rows.Next() {
		var entry atc.LandingEntry
		var settledAt sql.NullTime
		if err := rows.Scan(&entry.ID, &entry.Commit, &entry.State, &entry.SubmittedBy, &entry.SubmittedAt, &settledAt, &entry.SettleReason, &entry.ComposeRun, &entry.LandRun); err != nil {
			return status, err
		}
		if settledAt.Valid {
			t := settledAt.Time
			entry.SettledAt = &t
		}
		status.Entries = append(status.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return status, err
	}
	err = psql.Select("fails", "last_error").From("landing_intents").
		Where(sq.And{sq.Eq{"queue_id": queue.ID}, sq.NotEq{"state": LandingIntentDone}}).
		OrderBy("id DESC").Limit(1).RunWith(f.conn).QueryRowContext(ctx).Scan(&status.FailedLands, &status.LastError)
	if err != nil && err != sql.ErrNoRows {
		return status, err
	}
	return status, nil
}
