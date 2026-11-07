package landing

import (
	"context"
	"errors"
	"fmt"

	"code.cloudfoundry.org/lager/v3"
	"github.com/concourse/concourse/atc"
	"github.com/concourse/concourse/atc/db"
	"github.com/concourse/concourse/atc/runs"
)

// Gates (ADR-0011). Once a candidate's compose Run has succeeded, the queue
// admits one Run per gate it declares, all in one transaction, each binding
// the compose Run's composition. A gate passes when its Run succeeds; when
// every gate has passed the candidate lands. A failed Run is a red verdict
// and ejects the entry naming the gate and its Run. A Run that errors,
// aborts, vanishes or overruns its wait is no verdict: the gate gets one more
// Run, and a second no verdict in a row pauses the queue until it is
// resumed. A composition that is no longer bindable when a gate Run is
// admitted is the moved verdict: the entry is composed again.

// noVerdictRetries is how many times a gate's Run may give no verdict before
// the queue pauses on it: one retry, then pause.
const noVerdictRetries = 1

// admitGates admits the candidate's gate Runs and records them, taking the
// intent to gating, in the port's transaction: all of them or none.
func (e *Engine) admitGates(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent) (bool, error) {
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
	admissions := make([]db.LandingGateAdmission, 0, len(queue.Config.Gates))
	for ordinal, gate := range queue.Config.Gates {
		run, admitted, err := e.admitGate(ctx, tx, queue, entry, intent, ordinal, gate.Template, 1)
		if err != nil || !admitted {
			return e.notAdmitted(ctx, tx, intent, err)
		}
		admissions = append(admissions, db.LandingGateAdmission{Ordinal: ordinal, Template: gate.Template, Wait: gate.WaitDuration(), RunID: run.ID})
	}
	recorded, err := e.Queues.RecordGates(ctx, dbTx, intent, admissions)
	if err != nil || !recorded {
		return true, err
	}
	return true, tx.Commit()
}

// admitDue admits the next Run of every gate that is due one, and records
// each, in the port's transaction.
func (e *Engine) admitDue(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent, due []db.LandingGateRow) (bool, error) {
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
	for _, gate := range due {
		run, admitted, err := e.admitGate(ctx, tx, queue, entry, intent, gate.Ordinal, gate.Template, gate.Attempts+1)
		if err != nil || !admitted {
			return e.notAdmitted(ctx, tx, intent, err)
		}
		recorded, err := e.Queues.RecordGateRun(ctx, dbTx, gate, run.ID)
		if err != nil || !recorded {
			return true, err
		}
	}
	return true, tx.Commit()
}

// errComposeMoved is admitGate's moved verdict: the compose Run's
// composition is no longer bindable.
var errComposeMoved = errors.New("composition no longer bindable")

// admitGate admits one gate Run binding the compose Run's composition. False
// with no error is a refusal the next pass may not repeat.
func (e *Engine) admitGate(ctx context.Context, tx runs.Tx, queue db.LandingQueue, entry db.LandingEntryRow, intent db.LandingIntent, ordinal int, template string, attempt int) (runs.Run, bool, error) {
	run, _, err := e.Port.AdmitVersionedRun(ctx, tx, runs.Admission{
		Template:  runs.TemplateRef{Team: queue.TeamName, Pipeline: atc.PipelineRef{Name: template}},
		Params:    runParams(queue, entry),
		Inputs:    map[string]atc.RunInputSource{"composition": {RunID: intent.ComposeRunID, Result: "composition"}},
		Principal: runs.Principal{Queue: &runs.QueuePrincipal{TeamName: queue.TeamName, QueueName: queue.Name}},

		ContractKey: contractKey(fmt.Sprintf("gate%d", ordinal), entry.EntryID, intent.ComposeRunID, attempt),
	})
	switch {
	case errors.Is(err, atc.ErrRunInputUnavailable):
		return runs.Run{}, false, errComposeMoved
	case errors.Is(err, atc.ErrInvalidRunInputs):
		return runs.Run{}, false, fmt.Errorf("gate template %q does not declare the composition run input: %w", template, err)
	case err != nil:
		e.Logger.Info("landing-gate-admission-refused", lager.Data{"queue": queue.Name, "entry": entry.EntryID, "gate": template, "error": err.Error()})
		return runs.Run{}, false, nil
	}
	return run, true, nil
}

// notAdmitted ends a gate admission that did not go through: the port's
// transaction is rolled back, and a moved composition is composed again.
func (e *Engine) notAdmitted(ctx context.Context, tx runs.Transaction, intent db.LandingIntent, err error) (bool, error) {
	tx.Rollback()
	if errors.Is(err, errComposeMoved) {
		return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
			_, err := e.Queues.Recompose(ctx, tx, intent, fmt.Sprintf("compose Run %d's composition is no longer bindable for a gate; composing again", intent.ComposeRunID))
			return false, err
		})
	}
	return true, err
}

// settleGates reads each gate's Run and settles the ones that finished, each
// in its own transaction, then lands the candidate if every gate passed or
// admits the gates that are due a Run.
func (e *Engine) settleGates(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent) (bool, error) {
	gates, err := e.Queues.Gates(ctx, intent.ComposeRunID)
	if err != nil {
		return true, err
	}
	for _, gate := range gates {
		if gate.Verdict != atc.LandingGatePending || gate.RunID == nil {
			continue
		}
		open, held, err := e.settleGate(ctx, queue, intent, gate, gates)
		if err != nil || !open || held {
			return open, err
		}
	}
	gates, err = e.Queues.Gates(ctx, intent.ComposeRunID)
	if err != nil {
		return true, err
	}
	passed := 0
	var due []db.LandingGateRow
	for _, gate := range gates {
		switch {
		case gate.Verdict == atc.LandingGatePassed:
			passed++
		case gate.Verdict == atc.LandingGatePending && gate.RunID == nil:
			due = append(due, gate)
		}
	}
	if passed == len(gates) {
		return e.admitLand(ctx, queue, intent)
	}
	if len(due) > 0 {
		return e.admitDue(ctx, queue, intent, due)
	}
	return true, nil
}

// settleGate settles one gate by its Run's outcome, if it has one. It
// reports whether the intent is still open and whether the queue is now
// paused on this gate.
func (e *Engine) settleGate(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent, gate db.LandingGateRow, gates []db.LandingGateRow) (bool, bool, error) {
	run, found, err := e.Runs.GetRunByID(*gate.RunID)
	if err != nil {
		return true, false, err
	}
	if !found {
		return e.noVerdict(ctx, queue, intent, gate, fmt.Sprintf("gate %s: its Run is gone", gate.Template), false)
	}
	name := fmt.Sprintf("gate %s Run #%d", gate.Template, run.Number())
	switch status := run.Status(); {
	case status == atc.RunStatusRunning && gate.Overdue:
		return e.noVerdict(ctx, queue, intent, gate, fmt.Sprintf("%s did not finish within %s", name, gate.Wait), true)
	case status == atc.RunStatusRunning:
		return true, false, nil
	case status == atc.RunStatusSucceeded:
		open, err := e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
			_, err := e.Queues.PassGate(ctx, tx, gate)
			return true, err
		})
		return open, false, err
	case status == atc.RunStatusFailed:
		open, err := e.red(ctx, queue, intent, gate, gates, name+" failed")
		return open, false, err
	default:
		return e.noVerdict(ctx, queue, intent, gate, fmt.Sprintf("%s %s", name, status), false)
	}
}

// red ejects the entry on a gate's red verdict, naming the gate and its Run,
// and stops its other gates' Runs, whose verdicts no longer count: one
// transaction, keyed by the red Run.
func (e *Engine) red(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent, gate db.LandingGateRow, gates []db.LandingGateRow, reason string) (bool, error) {
	return e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
		failed, err := e.Queues.FailGate(ctx, tx, gate, reason)
		if err != nil || !failed {
			return true, err
		}
		for _, other := range gates {
			if other.ID == gate.ID || other.Verdict != atc.LandingGatePending || other.RunID == nil {
				continue
			}
			if err := e.cancel(ctx, tx, queue, *other.RunID, "the entry was ejected: "+reason); err != nil {
				return true, err
			}
		}
		_, err = e.Queues.EjectEntry(ctx, tx, intent, reason)
		return false, err
	})
}

// noVerdict records a gate Run that gave no verdict: the gate gets another
// Run, or, with no retry left, the queue pauses on it. An overdue Run is
// asked to stop in the same transaction.
func (e *Engine) noVerdict(ctx context.Context, queue db.LandingQueue, intent db.LandingIntent, gate db.LandingGateRow, reason string, overdue bool) (bool, bool, error) {
	held := gate.NoVerdicts >= noVerdictRetries
	open, err := e.inTx(ctx, intent, func(tx db.Tx, intent db.LandingIntent) (bool, error) {
		var settled bool
		var err error
		if held {
			settled, err = e.Queues.HoldGate(ctx, tx, intent, gate, reason+"; no verdict after one retry, so the queue is paused")
		} else {
			settled, err = e.Queues.RetryGate(ctx, tx, gate, reason+"; retrying once")
		}
		if err != nil || !settled {
			return true, err
		}
		if overdue {
			return true, e.cancel(ctx, tx, queue, *gate.RunID, reason)
		}
		return true, nil
	})
	return open, held, err
}

// cancel asks a gate Run to stop, as the queue. A Run already terminal or
// already asked is an outcome, not an error.
func (e *Engine) cancel(ctx context.Context, tx db.Tx, queue db.LandingQueue, runID int, reason string) error {
	if runes := []rune(reason); len(runes) > 500 {
		reason = string(runes[:500])
	}
	_, err := e.Runs.AcceptRunCancellation(ctx, tx, runID, "landing-queue/"+queue.TeamName+"/"+queue.Name, &reason)
	return err
}
