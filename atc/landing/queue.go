// Package landing is the landing queue's engine (ADR-0009): one bounded pass
// at a time over every queue, settling the Runs its intents name and
// admitting the next compose. It decides from Run statuses and one small
// verdict result; it never runs git and never holds a repository credential.
package landing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
	"github.com/concourse/concourse/hangar"
)

// Runs is what the engine reads of a Run: its status.
type Runs interface {
	GetRunByID(int) (db.PipelineRun, bool, error)
}

// Results reads a Run's small named result. runs.ResultReader is the
// production one; it opens its own transaction and locks the Run row, so the
// engine reads before it opens the settle transaction, never inside it.
type Results interface {
	Read(context.Context, int, string) (*hangar.CapturedTree, error)
}

// Verdict is the land Run's one-line result, verdict/verdict.json.
type Verdict struct {
	Outcome string `json:"outcome"`
	SHA     string `json:"sha,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

const (
	VerdictLanded = "landed"
	VerdictMoved  = "moved"
	VerdictFailed = "failed"
)

// Engine is the component's state: the tables, the port, and the epoch it
// admits under.
type Engine struct {
	Logger  lager.Logger
	Conn    db.DbConn
	Queues  db.LandingQueueFactory
	Runs    Runs
	Results Results
	Port    runs.Admitter
	Epoch   int64
}

// Run is one pass over every queue. An error on one queue is logged and the
// others still get their pass; the component's interval brings the next.
func (e *Engine) Run(ctx context.Context) error {
	queues, err := e.Queues.Queues(ctx)
	if err != nil {
		return err
	}
	for _, queue := range queues {
		if err := e.pass(ctx, queue); err != nil {
			e.Logger.Error("landing-queue-pass", err, lager.Data{"team": queue.TeamName, "queue": queue.Name})
		}
	}
	return nil
}

// pass settles every open intent of the queue whose Run is terminal, then
// composes the oldest queued entry if nothing is in flight.
func (e *Engine) pass(ctx context.Context, queue db.LandingQueue) error {
	intents, err := e.openIntents(ctx, queue)
	if err != nil {
		return err
	}
	inFlight := false
	for _, intent := range intents {
		open, err := e.settle(ctx, queue, intent)
		if err != nil {
			return err
		}
		inFlight = inFlight || open
	}
	if inFlight {
		return nil
	}
	return e.compose(ctx, queue)
}

func (e *Engine) openIntents(ctx context.Context, queue db.LandingQueue) ([]db.LandingIntent, error) {
	tx, err := e.Conn.Begin()
	if err != nil {
		return nil, err
	}
	defer db.Rollback(tx)
	intents, err := e.Queues.OpenIntents(ctx, tx, queue.ID)
	if err != nil {
		return nil, err
	}
	return intents, tx.Commit()
}

// settle advances one intent by what its Run did. It reports whether the
// intent is still open afterwards.
func (e *Engine) settle(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent) (bool, error) {
	switch intent.State {
	case db.LandingIntentComposing:
		return e.settleCompose(ctx, queue, intent)
	case db.LandingIntentLanding:
		return e.settleLand(ctx, queue, intent)
	}
	return false, nil
}

func (e *Engine) settleCompose(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent) (bool, error) {
	status, found, err := e.status(intent.ComposeRunID)
	if err != nil {
		return true, err
	}
	switch {
	case !found:
		return e.eject(ctx, intent, "compose Run is gone")
	case status == atc.RunStatusRunning:
		return true, nil
	case status == atc.RunStatusSucceeded:
		return e.admitLand(ctx, queue, intent)
	default:
		return e.eject(ctx, intent, fmt.Sprintf("compose Run %d %s", intent.ComposeRunID, status))
	}
}

func (e *Engine) settleLand(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent) (bool, error) {
	if intent.LandRunID == nil {
		// An earlier land Run did not land; admit another for the same candidate.
		return e.admitLand(ctx, queue, intent)
	}
	landRun := *intent.LandRunID
	status, found, err := e.status(landRun)
	if err != nil {
		return true, err
	}
	switch {
	case !found:
		return e.failLand(ctx, intent, "land Run is gone")
	case status == atc.RunStatusRunning:
		return true, nil
	case status != atc.RunStatusSucceeded:
		return e.failLand(ctx, intent, fmt.Sprintf("land Run %d %s", landRun, status))
	}
	// Before the settle transaction: the reader locks the Run row itself. A
	// verdict that cannot be read is a landing that cannot be proven: it counts
	// as failed and the candidate gets another land Run, rather than the queue
	// waiting on a result that may never come back.
	verdict, err := e.verdict(ctx, landRun)
	if err != nil {
		return e.failLand(ctx, intent, fmt.Sprintf("land Run %d: verdict unread: %s", landRun, err.Error()))
	}
	switch verdict.Outcome {
	case VerdictLanded:
		return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
			_, err := e.Queues.LandEntry(ctx, tx, intent, fmt.Sprintf("landed by Run %d as %s", landRun, verdict.SHA))
			return false, err
		})
	case VerdictMoved:
		return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
			_, err := e.Queues.Recompose(ctx, tx, intent, fmt.Sprintf("trunk moved under land Run %d; composing again", landRun))
			return false, err
		})
	default:
		return e.failLand(ctx, intent, fmt.Sprintf("land Run %d: %s %s", landRun, verdict.Outcome, verdict.Reason))
	}
}

// admitLand admits the land Run for a composed candidate, binding the compose
// Run's manifest and candidate results, and records it on the intent in the
// same transaction. A candidate whose results are no longer bindable is
// composed again; an admission the port refuses outright leaves the intent for
// the next pass, except a declaration mismatch, which is a defect to report.
func (e *Engine) admitLand(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent) (bool, error) {
	entry, err := e.Queues.Entry(ctx, intent.EntryRowID)
	if err != nil {
		return true, err
	}
	tx, err := e.Port.Begin(ctx)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	dbTx, ok := tx.(db.Tx)
	if !ok {
		return true, errors.New("the port's transaction is not the database's")
	}
	run, _, err := e.Port.AdmitVersionedRun(ctx, tx, runs.Admission{
		Template:  runs.TemplateRef{Team: queue.TeamName, Pipeline: atc.PipelineRef{Name: queue.Config.Land}},
		Params:    runParams(queue, entry),
		Inputs:    map[string]atc.RunInputSource{"manifest": {RunID: intent.ComposeRunID, Result: "manifest"}, "candidate": {RunID: intent.ComposeRunID, Result: "candidate"}},
		Principal: runs.Principal{Queue: &runs.QueuePrincipal{TeamName: queue.TeamName, QueueName: queue.Name}},

		ContractKey: contractKey("land", entry.EntryID, intent.ComposeRunID, intent.Fails),
		CausedByRun: &intent.ComposeRunID,
	}, e.Epoch)
	switch {
	case errors.Is(err, atc.ErrRunInputUnavailable):
		tx.Rollback()
		return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
			_, err := e.Queues.Recompose(ctx, tx, intent, fmt.Sprintf("compose Run %d's results are no longer bindable; composing again", intent.ComposeRunID))
			return false, err
		})
	case errors.Is(err, atc.ErrInvalidRunInputs):
		return true, fmt.Errorf("land template %q does not declare the manifest and candidate run inputs: %w", queue.Config.Land, err)
	case err != nil:
		e.Logger.Info("landing-land-admission-refused", lager.Data{"queue": queue.Name, "entry": entry.EntryID, "error": err.Error()})
		return true, nil
	}
	if err := e.Queues.RecordLand(ctx, dbTx, intent, run.ID); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

// compose admits the compose Run for the oldest queued entry and creates its
// intent in the port's transaction, so a crash between the two leaves neither.
func (e *Engine) compose(ctx context.Context, queue db.LandingQueue) error {
	tx, err := e.Port.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	dbTx, ok := tx.(db.Tx)
	if !ok {
		return errors.New("the port's transaction is not the database's")
	}
	entry, found, err := e.Queues.NextQueued(ctx, dbTx, queue.ID)
	if err != nil || !found {
		return err
	}
	attempts, err := e.Queues.ComposeAttempts(ctx, dbTx, entry.ID)
	if err != nil {
		return err
	}
	run, _, err := e.Port.AdmitVersionedRun(ctx, tx, runs.Admission{
		Template:  runs.TemplateRef{Team: queue.TeamName, Pipeline: atc.PipelineRef{Name: queue.Config.Compose}},
		Params:    runParams(queue, entry),
		Principal: runs.Principal{Queue: &runs.QueuePrincipal{TeamName: queue.TeamName, QueueName: queue.Name}},

		ContractKey: contractKey("compose", entry.EntryID, entry.ID, attempts),
	}, e.Epoch)
	if err != nil {
		e.Logger.Info("landing-compose-admission-refused", lager.Data{"queue": queue.Name, "entry": entry.EntryID, "error": err.Error()})
		return nil
	}
	if err := e.Queues.RecordCompose(ctx, dbTx, entry, run.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Engine) eject(ctx context.Context, intent db.LandingIntent, reason string) (bool, error) {
	return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
		_, err := e.Queues.EjectEntry(ctx, tx, intent, reason)
		return false, err
	})
}

func (e *Engine) failLand(ctx context.Context, intent db.LandingIntent, reason string) (bool, error) {
	return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
		_, err := e.Queues.FailLand(ctx, tx, intent, reason)
		return true, err
	})
}

// inTx re-reads the intent under lock and applies one settle, so a pass that
// raced another changes nothing it did not see.
func (e *Engine) inTx(ctx context.Context, intent db.LandingIntent, do func(db.Tx, db.LandingIntent) (bool, error)) (bool, error) {
	tx, err := e.Conn.Begin()
	if err != nil {
		return true, err
	}
	defer db.Rollback(tx)
	intents, err := e.Queues.OpenIntents(ctx, tx, intent.QueueID)
	if err != nil {
		return true, err
	}
	for _, current := range intents {
		if current.ID != intent.ID {
			continue
		}
		if current.State != intent.State || !sameLandRun(current.LandRunID, intent.LandRunID) {
			return true, nil
		}
		open, err := do(tx, current)
		if err != nil {
			return true, err
		}
		return open, tx.Commit()
	}
	return false, nil
}

func sameLandRun(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func (e *Engine) status(runID int) (atc.RunStatus, bool, error) {
	run, found, err := e.Runs.GetRunByID(runID)
	if err != nil || !found {
		return "", false, err
	}
	return run.Status(), true, nil
}

func (e *Engine) verdict(ctx context.Context, landRun int) (Verdict, error) {
	tree, err := e.Results.Read(ctx, landRun, "verdict")
	if err != nil {
		return Verdict{}, err
	}
	defer tree.Close()
	body, err := os.ReadFile(filepath.Join(tree.Root, "verdict.json"))
	if err != nil {
		return Verdict{}, err
	}
	var verdict Verdict
	if err := json.Unmarshal(body, &verdict); err != nil {
		return Verdict{}, fmt.Errorf("verdict.json: %w", err)
	}
	return verdict, nil
}

func runParams(queue db.LandingQueue, entry db.LandingEntryRow) atc.RunParams {
	return atc.RunParams{
		"repository": queue.Config.Repository,
		"trunk":      queue.Config.Trunk,
		"entries":    entry.EntryID + "=" + entry.Commit,
	}
}

// contractKey is the port's idempotency key for one admission: the kind, the
// entry, its row and the attempt, so a pass repeated after a crash replays the
// Run it admitted and a new attempt admits a new one. The alphabet is the port's.
func contractKey(kind, entryID string, run, attempt int) string {
	key := fmt.Sprintf("landing-%s-%s-%d-%d", kind, entryID, run, attempt)
	key = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '~' || r == '-' {
			return r
		}
		return '-'
	}, key)
	if len(key) > 128 {
		key = key[:128]
	}
	return key
}
